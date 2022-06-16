package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	"github.com/aws/aws-sdk-go-v2/service/polly/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/hajimehoshi/oto/v2"
	log "github.com/sirupsen/logrus"
	"github.com/tosone/minimp3"
)

//const MAX_CHAR_COUNT = 200_000

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
	flag.StringVar(&outputFile, "output", "", "path the save the mp3, this will NOT play the audio")

	flag.Parse()

	var ctx = context.Background()

	// validate input
	if strings.TrimSpace(s3Bucket) == "" {
		log.Fatal("s3 bucket not spcecified")
	}
	var text = strings.TrimSpace(inputFile)
	var err error
	if strings.TrimSpace(inputFile) == "" {
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

	// output switch
	if strings.TrimSpace(outputFile) == "" {
		if err := writeFile(voice.Body, outputFile); err != nil {
			log.Fatalf("error writing file %v", err)
		}
	} else {
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

// default, us-west-2, Matthew
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

func writeFile(sound io.ReadCloser, filename string) error {
	outFile, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("Got error creating speech.mp3: %v", err)
	}

	_, err = io.Copy(outFile, sound)
	if err != nil {
		return fmt.Errorf("error writing mp3: %v", err)
	}

	if err := outFile.Close(); err != nil {
		return fmt.Errorf("error closing file: %v", err)
	}
	return nil
}

func play(sound io.ReadCloser) error {
	var err error

	var dec *minimp3.Decoder
	if dec, err = minimp3.NewDecoder(sound); err != nil {
		return fmt.Errorf("minimp3.NewDecoder: %w", err)
	}
	started := dec.Started()
	<-started

	log.Infof("Convert audio sample rate: %d, channels: %d\n", dec.SampleRate, dec.Channels)

	var context *oto.Context
	if context, err = oto.NewContext(dec.SampleRate, dec.Channels, 2, 1024); err != nil {
		return fmt.Errorf("oto.NewContext: %w", err)
	}

	var waitForPlayOver = new(sync.WaitGroup)
	waitForPlayOver.Add(1)

	var player = context.NewPlayer()

	go func() {
		for {
			var data = make([]byte, 1024)
			_, err := dec.Read(data)
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			if _, err := player.Write(data); err != nil {
				log.Fatalf("error feeding player %v", err) // TODO handle this better
			}
		}
		log.Info("Audio complete.")
		waitForPlayOver.Done()
	}()
	waitForPlayOver.Wait()

	<-time.After(time.Second)
	dec.Close()

	if err := player.Close(); err != nil {
		return fmt.Errorf("error closing player: %w", err)
	}
	return nil
}
