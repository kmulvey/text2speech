# text2speech
[![text2speech](https://github.com/kmulvey/text2speech/actions/workflows/release_build.yml/badge.svg)](https://github.com/kmulvey/text2speech/actions/workflows/release_build.yml)

Convert text to speech using [AWS Polly](https://aws.amazon.com/polly/).

## Dependencies
This depends on [oto](https://github.com/hajimehoshi/oto#prerequisite) which has some prerequisites.

## Example
```
go clean
go build -v -ldflags="-s -w" . 
echo "The saddest aspect of life right now is that science gathers knowledge faster than society gathers wisdom." | ./text2speech -bucket your-s3-bucket

# print help:
./text2speech -h
```
