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

func splitInput(fulltext string) []string {
	var numWords = len(strings.Fields(fulltext))

	if numWords < MAX_CHAR_COUNT {
		return []string{fulltext}
	}

	var result []string
	var fulltextWords = strings.FieldsFunc(fulltext, isSpace)
	var lastEndIndex int
	for i := MAX_CHAR_COUNT; i >= 0; i-- {
		if strings.HasSuffix(fulltextWords[i], ".") {
			result = append(result, strings.Join(fulltextWords[lastEndIndex:i+1], " "))
			lastEndIndex = i + 1
			i += MAX_CHAR_COUNT
			if i >= len(fulltextWords) {
				result = append(result, strings.Join(fulltextWords[lastEndIndex:], " "))
				break
			}
		}
	}

	return result
}

func isSpace(c rune) bool {
	return unicode.IsSpace(c)
}

func deleteFile(ctx context.Context, s3Client *s3.Client, bucket, key string) error {
	var _, err = s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

// EscapeFilePath escapes spaces in the filepath used for an exec() call
func EscapeFilePath(file string) string {
	var r = strings.NewReplacer(" ", `\ `, "(", `\(`, ")", `\)`, "'", `\'`, "&", `\&`, "@", `\@`)
	return r.Replace(file)
}
