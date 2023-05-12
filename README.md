# text2speech
[![text2speech](https://github.com/kmulvey/text2speech/actions/workflows/release_build.yml/badge.svg)](https://github.com/kmulvey/text2speech/actions/workflows/release_build.yml) [![Go Report Card](https://goreportcard.com/badge/github.com/kmulvey/text2speech)](https://goreportcard.com/report/github.com/kmulvey/text2speech) [![Go Reference](https://pkg.go.dev/badge/github.com/kmulvey/imageconvert.svg)](https://pkg.go.dev/github.com/kmulvey/imageconvert)

Convert text to speech using [AWS Polly](https://aws.amazon.com/polly/).

## Dependencies
This depends on [oto](https://github.com/hajimehoshi/oto#prerequisite) which has some prerequisites.
```
sudo dnf install alsa-lib-devel
```

## Examples
### Pipe text:
```
echo "The saddest aspect of life right now is that science gathers knowledge faster than society gathers wisdom." | ./text2speech -bucket your-s3-bucket
```
### File Input
`./text2speech -bucket your-s3-bucket -input text`

### MP3 output:
`./text2speech -bucket your-s3-bucket -input text -output audio.mp3  # this will only write the file, it will not play it`

### Displaying a dashboard to monitor progress
`./text2speech -bucket your-s3-bucket -input text -dashboard`

### Print help:
`./text2speech -h`
