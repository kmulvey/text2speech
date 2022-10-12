package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	"github.com/aws/aws-sdk-go-v2/service/polly/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
	"go.szostok.io/version"
	"go.szostok.io/version/printer"
)

type PlaybackProgress struct {
	Total   int
	Current int
}

const MAX_CHAR_COUNT = 100 //200_000
const DEFAULT_VOICE = "Matthew"

func main() {
	var ctx, cancel = context.WithCancel(context.Background())

	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// get user opts
	var s3Bucket string
	var awsProfile string
	var awsRegion string
	var voiceID string
	var inputFile string
	var outputFile string
	var v bool
	flag.StringVar(&s3Bucket, "bucket", "", "s3 bucket to put the mp3 files")
	flag.StringVar(&awsProfile, "profile", "default", "aws profile to use")
	flag.StringVar(&awsRegion, "region", "us-west-2", "aws region to use")
	flag.StringVar(&voiceID, "voice", "Matthew", "voice to use")
	flag.StringVar(&inputFile, "input", "", "path the input text file, if this is specified STDIN will be ignored")
	flag.StringVar(&outputFile, "output", "output.mp3", "path the save the mp3, this will NOT play the audio")
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

	// validate input
	if strings.TrimSpace(s3Bucket) == "" {
		log.Fatal("s3 bucket not spcecified")
	}

	if voiceID != DEFAULT_VOICE {
		var voices = types.VoiceId("").Values()
		var found bool
		for _, voice := range voices {
			if voice == types.VoiceId(voiceID) {
				found = true
				break
			}
		}
		if !found {
			log.Fatalf("VoiceID: %s is not an AWS Polly VoiceID", voiceID)
		}
	}

	var text string
	var err error
	if strings.TrimSpace(inputFile) != "" {
		b, err := os.ReadFile(strings.TrimSpace(inputFile))
		if err != nil {
			log.Fatalf("cannot read input file %s: %v", inputFile, err)
		}
		text = string(b)

	} else {
		text, err = readInput(os.Stdin)
		if err != nil {
			log.Fatalf("cannot read input %v", err)
		}
	}
	if text == "" {
		return
	}

	// create aws clients
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(awsProfile), config.WithRegion(awsRegion))
	if err != nil {
		log.Fatalf("failed to load SDK configuration, %v", err)
	}
	var pollyClient = polly.NewFromConfig(awsConfig)
	var s3Client = s3.NewFromConfig(awsConfig)
	var audioChan = make(chan *s3.GetObjectOutput, 5)
	var errors = make(chan error)
	var playbackProgress = make(chan PlaybackProgress)
	var logs = make(chan string)

	go playWithProgressBar(audioChan, playbackProgress, errors)
	go func() {
		if err := handleOutput(ctx, pollyClient, s3Client, audioChan, logs, s3Bucket, voiceID, text, outputFile); err != nil {
			log.Fatalf("handleOutput error: %s", err.Error())
		}
	}()

	go func() {
		<-errors
		cancel()
	}()

	if _, err := NewDashboard(ctx, cancel, playbackProgress, logs); err != nil {
		log.Fatalf("failed to create dashboard, %v", err)
	}
}

// handleOutput synthesizes text and either writes the result to a file or a channel for playing. File writing and playing are exclusize and is determined by cli flags.
func handleOutput(ctx context.Context, pollyClient *polly.Client, s3Client *s3.Client, audioChan chan *s3.GetObjectOutput, logs chan string, s3Bucket, voiceID, text, outputFile string) error {

	// splitting the input allows us to handle input that is larger than the max input size of polly (200k)
	var textSections = splitInput(text)
	logs <- fmt.Sprintf("input text has been slpit into %d sections in order to comply with polly limits. \n", len(textSections))

	for _, section := range textSections {
		voice, s3File, err := synthesizeText(ctx, pollyClient, s3Client, logs, s3Bucket, voiceID, section)
		if err != nil {
			return fmt.Errorf("error from synthesisText: %v", err)
		}

		// output switch
		if strings.TrimSpace(outputFile) != "output.mp3" {
			body, err := io.ReadAll(voice.Body)
			if err != nil {
				return fmt.Errorf("error reading voice.Body: %v", err)
			}
			if err := os.WriteFile(outputFile, body, 0775); err != nil {
				return fmt.Errorf("error writing file %v", err)
			}
		} else {
			audioChan <- voice
		}

		// clean up s3 bucket
		if err := deleteFile(ctx, s3Client, s3Bucket, s3File); err != nil {
			return fmt.Errorf("error deleting s3 files %v", err)
		}
	}
	close(audioChan)
	close(logs)
	return nil
}

// playWithProgressBar manages the progess bar and plays the audio
func playWithProgressBar(audioChan chan *s3.GetObjectOutput, playbackProgress chan PlaybackProgress, errors chan error) {

	for voice := range audioChan {

		// get the audio length using ffmpeg because polly doesnt return it
		var tempFile = "ffmpeg-detect-length-temp-file.mp3"
		ffmpegSound, err := io.ReadAll(voice.Body)
		if err != nil {
			errors <- fmt.Errorf("error reading voice.Body: %v", err)
			close(errors)
			return
		}

		voice.Body = io.NopCloser(bytes.NewBuffer(ffmpegSound))
		if err := os.WriteFile(tempFile, ffmpegSound, 0775); err != nil {
			errors <- fmt.Errorf("error writing file %v", err)
			close(errors)
			return
		}

		audioLength, err := getDuration(tempFile)
		if err != nil {
			errors <- fmt.Errorf("error getting length from ffmpeg: %v", err)
			close(errors)
			return
		}

		if err := os.RemoveAll(tempFile); err != nil {
			errors <- fmt.Errorf("error deleteing temp file %v", err)
			close(errors)
			return
		}

		// get the progress bar going
		go func() {
			for i := 0; i < audioLength; i++ {
				playbackProgress <- PlaybackProgress{
					Current: i,
					Total:   audioLength,
				}
				time.Sleep(time.Second)
			}
			close(playbackProgress)
		}()

		if err := play(voice.Body); err != nil {
			log.Fatalf("error playing audio %v", err)
		}
	}
	close(errors)
	close(playbackProgress)
}
