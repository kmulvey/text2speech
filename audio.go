package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/polly"
	"github.com/aws/aws-sdk-go-v2/service/polly/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/go-mp3"
)

// synthesizeText takes text and sends it to AWS polly for processing, the polly object containing the audio.
func synthesizeText(ctx context.Context, pollyClient *polly.Client, s3Client *s3.Client, logs chan string, bucket, voiceID, text string) (*s3.GetObjectOutput, string, error) {

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

		logs <- fmt.Sprintf("Synthesis running... status: %s, id: %s \n", sTask.SynthesisTask.TaskStatus, *sTask.SynthesisTask.TaskId)

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

// play does just that (using oto)
func play(sound io.Reader) error {

	// Decode file
	var decodedMp3, err = mp3.NewDecoder(sound)
	if err != nil {
		panic("mp3.NewDecoder failed: " + err.Error())
	}

	var options = &oto.NewContextOptions{
		SampleRate:   decodedMp3.SampleRate(),
		ChannelCount: 2,
		Format:       oto.FormatSignedInt16LE,
	}
	otoCtx, ready, err := oto.NewContext(options)
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

// getDuration uses ffmpeg to get the duration of the audio because its not
// returned by polly. The return value is seconds. Hopefully polly adds the
// duration in the return data and we dont have to do this anymore.
func getDuration(filename string) (int, error) {

	if _, err := os.Stat(filename); err != nil {
		return 0, err
	}

	var imagePath = EscapeFilePath(filename)
	//nolint:gosec
	out, err := exec.Command("bash", "-c", fmt.Sprintf("ffmpeg -hide_banner -i %s -f null /dev/null", imagePath)).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("error running ffmpeg on image: %s, error: %s, output: %s", imagePath, err.Error(), out)
	}

	// create regex and find the duration in the ffmpeg output
	var durationRegex = regexp.MustCompile(`Duration:\s\d\d:\d\d:\d\d.\d\d`)
	var match = durationRegex.FindString(string(out))
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
