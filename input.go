package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// readInput reads in chunks and returns a string
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

// splitInput splits the text input into chunks of at most MAX_CHAR_COUNT
// characters, which is the current polly limit per job. It prefers to split
// at sentence boundaries (". "), falling back to the last whitespace character.
func splitInput(fulltext string) []string {
	if len(fulltext) <= MAX_CHAR_COUNT {
		return []string{fulltext}
	}

	var result []string
	remaining := fulltext
	for len(remaining) > MAX_CHAR_COUNT {
		chunk := remaining[:MAX_CHAR_COUNT]

		// prefer splitting at a sentence boundary
		splitAt := strings.LastIndex(chunk, ". ")
		if splitAt < 0 {
			// fall back to last whitespace
			splitAt = strings.LastIndexFunc(chunk, unicode.IsSpace)
		}
		if splitAt < 0 {
			// hard split as last resort
			splitAt = MAX_CHAR_COUNT
		} else {
			splitAt++ // include the trailing period/space in this chunk
		}

		result = append(result, strings.TrimSpace(remaining[:splitAt]))
		remaining = strings.TrimSpace(remaining[splitAt:])
	}
	if remaining != "" {
		result = append(result, remaining)
	}
	return result
}

// deleteS3File deletes the file that polly writes to s3 after we are done playing it.
func deleteS3File(ctx context.Context, s3Client *s3.Client, bucket, key string) error {
	var _, err = s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 delete object: %w", err)
	}
	return nil
}

// EscapeFilePath escapes spaces in the filepath used for an exec() call
func EscapeFilePath(file string) string {
	var r = strings.NewReplacer(" ", `\ `, "(", `\(`, ")", `\)`, "'", `\'`, "&", `\&`, "@", `\@`)
	return r.Replace(file)
}
