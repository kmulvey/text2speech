package main

import (
	"bufio"
	"context"
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

const bucket = "kmulvey-polly"

func main() {
	var ctx = context.Background()

	text, err := ioutil.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		log.Fatalf("failed to load SDK configuration, %v", err)
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile("default"), config.WithRegion("us-west-2"))
	if err != nil {
		log.Fatalf("failed to load SDK configuration, %v", err)
	}

	pollyClient := polly.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	inputTask := &polly.StartSpeechSynthesisTaskInput{OutputFormat: "mp3", OutputS3BucketName: aws.String(bucket), Text: aws.String(string(text)), VoiceId: "Matthew"}
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

	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    &path[2],
	})

	play(resp.Body)

	/*
		outFile, err := os.Create("speech.mp3")
		if err != nil {
			log.Fatalf("Got error creating speech.mp3: %v", err)
		}

		_, err = io.Copy(outFile, resp.Body)
		if err != nil {
			log.Fatalf("error writing mp3: %v", err)
		}

		if err := outFile.Close(); err != nil {
			log.Fatalf("error closing file: %v", err)
		}
	*/
}

func play(sound io.ReadCloser) {
	var err error

	var dec *minimp3.Decoder
	if dec, err = minimp3.NewDecoder(sound); err != nil {
		log.Fatal(err)
	}
	started := dec.Started()
	<-started

	log.Printf("Convert audio sample rate: %d, channels: %d\n", dec.SampleRate, dec.Channels)

	var context *oto.Context
	if context, err = oto.NewContext(dec.SampleRate, dec.Channels, 2, 1024); err != nil {
		log.Fatal(err)
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
		log.Println("over play.")
		waitForPlayOver.Done()
	}()
	waitForPlayOver.Wait()

	<-time.After(time.Second)
	dec.Close()
	if err = player.Close(); err != nil {
		log.Fatal(err)
	}
}
