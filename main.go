package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	"github.com/aws/aws-sdk-go-v2/service/polly/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
	"go.szostok.io/version"
	"go.szostok.io/version/printer"
)

// PlaybackProgress represents how far we have gotten in playing the audio
type PlaybackProgress struct {
	Total        int // section total in seconds
	Current      int // section elapsed in seconds
	GrandTotal   int // running sum of all section durations resolved so far
	GrandElapsed int // total seconds elapsed across all sections
}

func (p *PlaybackProgress) String() string {
	return fmt.Sprintf("GrandElapsed: %d, GrandTotal: %d", p.GrandElapsed, p.GrandTotal)
}

const MAX_CHAR_COUNT = 100_000  // StartSpeechSynthesisTask limit (async) is 100k chars
const DEFAULT_VOICE = "Matthew" // this can be overridden with cli flags

type cliOpts struct {
	s3Bucket   string
	awsProfile string
	awsRegion  string
	voiceID    string
	inputFile  string
	outputFile string
	dashboard  bool
}

func parseFlags() cliOpts {
	var opts cliOpts
	var v bool
	flag.StringVar(&opts.s3Bucket, "bucket", "", "s3 bucket to put the mp3 files")
	flag.StringVar(&opts.awsProfile, "profile", "default", "aws profile to use")
	flag.StringVar(&opts.awsRegion, "region", "us-west-2", "aws region to use")
	flag.StringVar(&opts.voiceID, "voice", "Matthew", "voice to use")
	flag.StringVar(&opts.inputFile, "input", "", "path the input text file, if this is specified STDIN will be ignored")
	flag.StringVar(&opts.outputFile, "output", "output.mp3", "path the save the mp3, this will NOT play the audio")
	flag.BoolVar(&opts.dashboard, "dashboard", false, "use a terminal dashboard")
	flag.BoolVar(&v, "version", false, "print version")
	flag.BoolVar(&v, "v", false, "print version")
	flag.Parse()
	if v {
		var verPrinter = printer.New()
		var info = version.Get()
		if err := verPrinter.PrintInfo(os.Stdout, info); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	return opts
}

func validateOpts(opts cliOpts) {
	if strings.TrimSpace(opts.s3Bucket) == "" {
		log.Fatal("s3 bucket not spcecified")
	}
	if opts.voiceID != DEFAULT_VOICE {
		if !slices.Contains(types.VoiceId("").Values(), types.VoiceId(opts.voiceID)) {
			log.Fatalf("VoiceID: %s is not an AWS Polly VoiceID", opts.voiceID)
		}
	}
}

func getInputText(inputFile string) string {
	if strings.TrimSpace(inputFile) != "" {
		b, err := os.ReadFile(strings.TrimSpace(inputFile))
		if err != nil {
			log.Fatalf("cannot read input file %s: %v", inputFile, err)
		}
		return string(b)
	}
	text, err := readInput(os.Stdin)
	if err != nil {
		log.Fatalf("cannot read input %v", err)
	}
	return text
}

func run(ctx context.Context, cancel context.CancelFunc, opts cliOpts, text string) {
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(opts.awsProfile), config.WithRegion(opts.awsRegion))
	if err != nil {
		log.Fatalf("failed to load SDK configuration, %v", err)
	}
	var pollyClient = polly.NewFromConfig(awsConfig)
	var s3Client = s3.NewFromConfig(awsConfig)
	var audioChan = make(chan *s3.GetObjectOutput, 5)
	var errors = make(chan error)
	var playbackProgress = make(chan PlaybackProgress)
	var logs = make(chan string, 32)
	var pauseChan = make(chan bool, 1)

	if !opts.dashboard {
		go logOutput(playbackProgress, logs)
	}

	go playWithProgressBar(audioChan, playbackProgress, errors, pauseChan)
	// Use a buffered channel so the goroutine never blocks even if run() has already returned.
	handleErrCh := make(chan error, 1)
	go func() {
		handleErrCh <- handleOutput(ctx, pollyClient, s3Client, audioChan, logs, opts.s3Bucket, opts.voiceID, text, opts.outputFile)
	}()

	if !opts.dashboard {
		if err := <-errors; err != nil {
			log.Error(err)
		}
		if err := <-handleErrCh; err != nil {
			log.Fatal(err)
		}
		cancel()
		return
	}

	go func() {
		if err := <-errors; err != nil {
			log.Error(err)
		}
		cancel()
	}()
	if err := NewDashboard(ctx, cancel, playbackProgress, logs, pauseChan); err != nil {
		log.Fatalf("failed to create dashboard, %v", err)
	}
	// Terminal is now restored. Check whether handleOutput reported an error
	// and surface it to the user. This must be done here (not in a goroutine)
	// to guarantee it runs before main() exits.
	if err := <-handleErrCh; err != nil {
		log.Error(err)
	}
}

func main() {
	var ctx, cancel = context.WithCancel(context.Background())
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	opts := parseFlags()
	validateOpts(opts)
	text := getInputText(opts.inputFile)
	if text == "" {
		return
	}
	run(ctx, cancel, opts, text)
}

// handleOutput synthesizes text and either writes the result to a file or a channel for playing. File writing and playing are exclusize and is determined by cli flags.
func handleOutput(ctx context.Context, pollyClient *polly.Client, s3Client *s3.Client, audioChan chan *s3.GetObjectOutput, logs chan string, s3Bucket, voiceID, text, outputFile string) error {
	// Always close both channels so consumers (playWithProgressBar, dashboard log
	// pane) are never left blocked waiting when we return early with an error.
	defer close(audioChan)
	defer close(logs)

	// splitting the input allows us to handle input that is larger than the max input size of polly (200k)
	var textSections = splitInput(text)
	logs <- fmt.Sprintf("The input text has been slpit into %d sections in order to comply with polly limits. \n", len(textSections))

	for _, section := range textSections {
		voice, s3File, err := synthesizeText(ctx, pollyClient, s3Client, logs, s3Bucket, voiceID, section)
		if err != nil {
			logs <- fmt.Sprintf("ERROR: %v\n", err)
			return fmt.Errorf("error from synthesisText: %w", err)
		}

		// output switch
		if strings.TrimSpace(outputFile) != "output.mp3" {
			body, err := io.ReadAll(voice.Body)
			if err != nil {
				return fmt.Errorf("error reading voice.Body: %w", err)
			}
			//nolint:gosec
			if err := os.WriteFile(outputFile, body, 0775); err != nil {
				return fmt.Errorf("error writing file: %w", err)
			}
		} else {
			audioChan <- voice
		}

		// clean up s3 bucket
		if err := deleteS3File(ctx, s3Client, s3Bucket, s3File); err != nil {
			return fmt.Errorf("error deleting s3 files: %w", err)
		}
	}
	return nil
}

// playWithProgressBar manages the progess bar and plays the audio
func playWithProgressBar(audioChan chan *s3.GetObjectOutput, playbackProgress chan PlaybackProgress, errors chan error, pauseChan <-chan bool) {
	var completedSeconds int
	var grandTotal int
	var paused atomic.Bool

	// Forward pause signals from the channel into the shared atomic flag so both
	// play() and the progress ticker goroutine can read it without racing.
	go func() {
		for p := range pauseChan {
			paused.Store(p)
		}
	}()

	for voice := range audioChan {
		body, audioLength, err := prepareAudio(voice)
		if err != nil {
			errors <- err
			close(errors)
			return
		}

		grandTotal += audioLength
		sectionLen := audioLength
		baseElapsed := completedSeconds
		total := grandTotal

		// send per-second progress ticks for this section, freezing while paused
		go func() {
			for i := range sectionLen {
				for paused.Load() {
					time.Sleep(50 * time.Millisecond)
				}
				playbackProgress <- PlaybackProgress{
					Current:      i,
					Total:        sectionLen,
					GrandElapsed: baseElapsed + i,
					GrandTotal:   total,
				}
				time.Sleep(time.Second)
			}
		}()

		if err := play(body, &paused); err != nil {
			errors <- fmt.Errorf("error playing audio: %w", err)
			close(errors)
			close(playbackProgress)
			return
		}

		completedSeconds += audioLength
	}
	close(errors)
	close(playbackProgress)
}

// prepareAudio reads the voice body, writes it to a temp file to determine its
// duration via ffmpeg, then returns a fresh reader and duration in seconds.
func prepareAudio(voice *s3.GetObjectOutput) (io.Reader, int, error) {
	const tempFile = "ffmpeg-detect-length-temp-file.mp3"

	body, err := io.ReadAll(voice.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("error reading voice.Body: %w", err)
	}
	//nolint:gosec
	if err := os.WriteFile(tempFile, body, 0775); err != nil {
		return nil, 0, fmt.Errorf("error writing temp file: %w", err)
	}
	audioLength, err := getDuration(tempFile)
	if err != nil {
		return nil, 0, fmt.Errorf("error getting length from ffmpeg: %w", err)
	}
	if err := os.RemoveAll(tempFile); err != nil {
		return nil, 0, fmt.Errorf("error deleting temp file: %w", err)
	}
	return io.NopCloser(bytes.NewBuffer(body)), audioLength, nil
}

// logOutput is used to print logs if the dashboard is not in use.
func logOutput(playbackProgress chan PlaybackProgress, logs chan string) {
	for playbackProgress != nil {
		select {
		case progress, ok := <-playbackProgress:
			if !ok {
				playbackProgress = nil
				continue
			}
			var pct float64
			// dont divide by 0
			if progress.GrandElapsed > 0 && progress.GrandTotal > 0 {
				pct = (float64(progress.GrandElapsed) / float64(progress.GrandTotal)) * 100.0
			}
			log.Infof("Progress: %.2f%%", pct)
		case logMsg, ok := <-logs:
			if !ok {
				logs = nil
				continue
			}
			log.Info(logMsg)
		}
	}
}
