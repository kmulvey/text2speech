package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	"github.com/aws/aws-sdk-go-v2/service/polly/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hajimehoshi/go-mp3"
	"github.com/hajimehoshi/oto/v2"
	"github.com/schollz/progressbar/v3"
	log "github.com/sirupsen/logrus"
)

// TODO handle >200k words
// const MAX_CHAR_COUNT = 200_000
const DEFAULT_VOICE = "Matthew"

func main() {
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
	flag.StringVar(&s3Bucket, "bucket", "", "s3 bucket to put the mp3 files")
	flag.StringVar(&awsProfile, "profile", "default", "aws profile to use")
	flag.StringVar(&awsRegion, "region", "us-west-2", "aws region to use")
	flag.StringVar(&voiceID, "voice", "Matthew", "voice to use")
	flag.StringVar(&inputFile, "input", "", "path the input text file, if this is specified STDIN will be ignored")
	flag.StringVar(&outputFile, "output", "speech.mp3", "path the save the mp3, this will NOT play the audio")

	flag.Parse()

	var ctx = context.Background()

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

	//var numWords = len(strings.Fields(builder.String()))
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

	// DO IT!
	voice, s3File, err := synthesisText(ctx, pollyClient, s3Client, s3Bucket, voiceID, text)
	if err != nil {
		log.Fatalf("error from synthesisText: %v", err)
	}

	// get the audio length using ffmpeg because polly doesnt return it
	ffmpegSound, err := io.ReadAll(voice.Body)
	if err != nil {
		log.Fatalf("error reading voice.Body: %v", err)
	}
	voice.Body = ioutil.NopCloser(bytes.NewBuffer(ffmpegSound))
	if err := os.WriteFile(outputFile, ffmpegSound, 0775); err != nil {
		log.Fatalf("error writing file %v", err)
	}
	audioLength, err := getLength(outputFile)
	if err != nil {
		log.Fatalf("error getting length from ffmpeg: %v", err)
	}

	// get the progress bar going
	go func() {
		bar := progressbar.NewOptions(100, progressbar.OptionSetPredictTime(false), progressbar.OptionFullWidth())
		var i float64
		var pct float64
		var total float64 = float64(audioLength)
		for i = 0.0; i < total; i++ {
			pct = (i / total) * 100
			err = bar.Set(int(pct))
			if err != nil {
				log.Fatal("error setting bar: ", err.Error())
			}
			time.Sleep(time.Second)
		}
	}()

	// output switch
	if strings.TrimSpace(outputFile) == "speech.mp3" {
		if err := os.RemoveAll(outputFile); err != nil {
			log.Fatalf("error removing file %v", err)
		}
		if err := play(voice.Body); err != nil {
			log.Fatalf("error playing audio %v", err)
		}
	}

	// clean up s3 bucket
	if err := deleteFile(ctx, s3Client, s3Bucket, s3File); err != nil {
		log.Fatalf("error deleting s3 files %v", err)
	}
}

func readInput(reader io.Reader) (string, error) {

	var r = bufio.NewReader(reader)
	var buf = make([]byte, 0, 4*1024)
	var builder = strings.Builder{}
	for {
		n, err := r.Read(buf[:cap(buf)])
		buf = buf[:n]
		if n == 0 {
			if err == nil {
				continue
			}
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("input buffer error: %w", err)
		}

		builder.Write(buf)

		if err != nil && err != io.EOF {
			return "", fmt.Errorf("input buffer error: %w", err)
		}
	}

	return strings.TrimSpace(builder.String()), nil
}

func synthesisText(ctx context.Context, pollyClient *polly.Client, s3Client *s3.Client, bucket, voiceID, text string) (*s3.GetObjectOutput, string, error) {

	inputTask := &polly.StartSpeechSynthesisTaskInput{OutputFormat: "mp3", OutputS3BucketName: aws.String(bucket), Text: aws.String(text), VoiceId: types.VoiceId(voiceID)}
	task, err := pollyClient.StartSpeechSynthesisTask(ctx, inputTask)
	if err != nil {
		return nil, "", fmt.Errorf("failed to convert to speech, %w", err)
	}

	var fileURI string
	for {
		var sTask, err = pollyClient.GetSpeechSynthesisTask(ctx, &polly.GetSpeechSynthesisTaskInput{TaskId: task.SynthesisTask.TaskId})
		if err != nil {
			return nil, "", fmt.Errorf("failed to get task status, %w", err)
		}

		if sTask.SynthesisTask.TaskStatus == types.TaskStatusCompleted {
			fileURI = *sTask.SynthesisTask.OutputUri
			break
		} else if sTask.SynthesisTask.TaskStatus == types.TaskStatusFailed {
			return nil, "", fmt.Errorf("task failed: err: %w;  reason: %s", err, *sTask.SynthesisTask.TaskStatusReason)
		}

		log.WithFields(log.Fields{
			"status": sTask.SynthesisTask.TaskStatus,
			"id":     *sTask.SynthesisTask.TaskId,
		}).Info("Synthesis running...")

		time.Sleep(time.Second * 5)
	}

	s3File, err := url.Parse(fileURI)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse s3 uri, %w", err)
	}

	var path = strings.Split(s3File.Path, "/")
	if len(path) != 3 {
		return nil, "", fmt.Errorf("s3 path is not three elements: %d, %+v", len(path), path)
	}

	voice, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    &path[2],
	})

	return voice, path[2], err
}

func deleteFile(ctx context.Context, s3Client *s3.Client, bucket, key string) error {
	var _, err = s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

func play(sound io.Reader) error {

	// Decode file
	var decodedMp3, err = mp3.NewDecoder(sound)
	if err != nil {
		panic("mp3.NewDecoder failed: " + err.Error())
	}

	otoCtx, ready, err := oto.NewContext(decodedMp3.SampleRate(), 2, 2)
	if err != nil {
		return fmt.Errorf("oto.NewContext: %w", err)
	}
	<-ready

	var player = otoCtx.NewPlayer(decodedMp3)
	player.Play()
	for player.IsPlaying() {
		time.Sleep(time.Millisecond)
	}

	if err := player.Close(); err != nil {
		return fmt.Errorf("error closing player: %w", err)
	}
	return nil
}

// getLength uses ffmpeg to get the duration of the audio because its not
// returned by polly. The return value is seconds.
func getLength(filename string) (int, error) {
	var imagePath = EscapeFilePath(filename)
	out, err := exec.Command("bash", "-c", fmt.Sprintf("ffmpeg -hide_banner -i %s -f null /dev/null", imagePath)).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("error running ffmpeg on image: %s, error: %s, output: %s", imagePath, err.Error(), out)
	}

	// create regex and find the duration in the ffmpeg output
	r, err := regexp.Compile(`Duration:\s\d\d:\d\d:\d\d.\d\d`)
	if err != nil {
		return 0, err
	}
	var match = r.FindString(string(out))
	if match == "" {
		return 0, errors.New("unable to get duration from ffmpeg")
	}
	match = strings.ReplaceAll(match, "Duration: ", "")

	// iterate through the string and parse the time: e.g. (00:04:40.66)
	var durationArr = strings.Split(match, ":")
	var duration int
	for i, digits := range durationArr {
		switch i {
		case 0:
			// hours
			num, err := strconv.Atoi(digits)
			if err != nil {
				return 0, err
			}
			duration = num * 3600
		case 1:
			// minutes
			num, err := strconv.Atoi(digits)
			if err != nil {
				return 0, err
			}
			duration += num * 60
		case 2:
			// seconds
			num, err := strconv.ParseFloat(digits, 32)
			if err != nil {
				return 0, err
			}
			duration += int(num)
		default:
			return 0, errors.New("could not parse duration")
		}
	}
	return duration, nil
}

// EscapeFilePath escapes spaces in the filepath used for an exec() call
func EscapeFilePath(file string) string {
	var r = strings.NewReplacer(" ", `\ `, "(", `\(`, ")", `\)`, "'", `\'`, "&", `\&`, "@", `\@`)
	return r.Replace(file)
}
