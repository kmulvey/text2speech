package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
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
	"github.com/hajimehoshi/oto"
	log "github.com/sirupsen/logrus"
	"github.com/tosone/minimp3"
)

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
	flag.StringVar(&s3Bucket, "bucket", "", "s3 bucket to put the mp3 files")
	flag.StringVar(&awsProfile, "profile", "default", "aws profile to use")
	flag.StringVar(&awsRegion, "region", "us-west-2", "aws region to use")
	flag.StringVar(&voiceID, "voice", "Matthew", "voice to use")

	flag.Parse()

	var ctx = context.Background()
}

// default, us-west-2, Matthew
func synthesisText(ctx context.Context, awsProfile, awsRegion, bucket, voiceID string) (*s3.GetObjectOutput, error) {

	text, err := ioutil.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		log.Fatalf("failed to load SDK configuration, %v", err)
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(awsProfile), config.WithRegion(awsRegion))
	if err != nil {
		log.Fatalf("failed to load SDK configuration, %v", err)
	}

	pollyClient := polly.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	inputTask := &polly.StartSpeechSynthesisTaskInput{OutputFormat: "mp3", OutputS3BucketName: aws.String(bucket), Text: aws.String(string(text)), VoiceId: types.VoiceId(voiceID)}
	task, err := pollyClient.StartSpeechSynthesisTask(ctx, inputTask)
	if err != nil {
		log.Fatalf("failed to convert to speech, %v", err)
	}

	var fileURI string
	for {
		var sTask, err = pollyClient.GetSpeechSynthesisTask(ctx, &polly.GetSpeechSynthesisTaskInput{TaskId: task.SynthesisTask.TaskId})
		if err != nil {
			log.Fatalf("failed to get task status, %v", err)
		}

		if sTask.SynthesisTask.TaskStatus == types.TaskStatusCompleted {
			fileURI = *sTask.SynthesisTask.OutputUri
			break
		} else if sTask.SynthesisTask.TaskStatus == types.TaskStatusFailed {
			log.Fatalf("task failed: err: %v;  reason: %s", err, *sTask.SynthesisTask.TaskStatusReason)
		}

		log.WithFields(log.Fields{
			"status": sTask.SynthesisTask.TaskStatus,
			"id":     *sTask.SynthesisTask.TaskId,
		}).Info("Synthesis running...")

		time.Sleep(time.Second * 2)
	}

	s3File, err := url.Parse(fileURI)
	if err != nil {
		log.Fatalf("failed to parse s3 uri, %v", err)
	}

	var path = strings.Split(s3File.Path, "/")
	if len(path) != 3 {
		log.Fatalf("s3 path is not three elements: %d, %+v", len(path), path)
	}

	return s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    &path[2],
	})
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
		fmt.Errorf("minimp3.NewDecoder: %w", err)
	}
	started := dec.Started()
	<-started

	log.Infof("Convert audio sample rate: %d, channels: %d\n", dec.SampleRate, dec.Channels)

	var context *oto.Context
	if context, err = oto.NewContext(dec.SampleRate, dec.Channels, 2, 1024); err != nil {
		fmt.Errorf("oto.NewContext: %w", err)
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
			player.Write(data)
		}
		log.Info("over play.")
		waitForPlayOver.Done()
	}()
	waitForPlayOver.Wait()

	<-time.After(time.Second)
	dec.Close()

	if err := player.Close(); err != nil {
		return fmt.Errorf("error closing player: %v", err)
	}
	return nil
}
