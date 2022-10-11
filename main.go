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
	"github.com/schollz/progressbar/v3"
	log "github.com/sirupsen/logrus"
	"go.szostok.io/version"
	"go.szostok.io/version/printer"
)

const MAX_CHAR_COUNT = 100 //200_000
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
	var v bool
	flag.StringVar(&s3Bucket, "bucket", "", "s3 bucket to put the mp3 files")
	flag.StringVar(&awsProfile, "profile", "default", "aws profile to use")
	flag.StringVar(&awsRegion, "region", "us-west-2", "aws region to use")
	flag.StringVar(&voiceID, "voice", "Matthew", "voice to use")
	flag.StringVar(&inputFile, "input", "", "path the input text file, if this is specified STDIN will be ignored")
	flag.StringVar(&outputFile, "output", "speech.mp3", "path the save the mp3, this will NOT play the audio")
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

	// splitting the input allows us to handle input that is larger than the max input size of polly (200k)
	var textSections = splitInput(text)
	log.Infof("input text has been slpit into %d sections in order to comply with polly limits", len(textSections))
	for _, section := range textSections {
		runSynthesis(ctx, pollyClient, s3Client, s3Bucket, voiceID, section, outputFile)
	}
}

func runSynthesis(ctx context.Context, pollyClient *polly.Client, s3Client *s3.Client, s3Bucket, voiceID, text, outputFile string) {
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
	voice.Body = io.NopCloser(bytes.NewBuffer(ffmpegSound))
	if err := os.WriteFile(outputFile, ffmpegSound, 0775); err != nil {
		log.Fatalf("error writing file %v", err)
	}
	audioLength, err := getDuration(outputFile)
	if err != nil {
		log.Fatalf("error getting length from ffmpeg: %v", err)
	}

	// get the progress bar going
	var progressBarDone = make(chan struct{})
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
		err = bar.Set(100)
		if err != nil {
			log.Fatal("error setting bar: ", err.Error())
		}
		fmt.Println()
		close(progressBarDone)
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
	<-progressBarDone
}
