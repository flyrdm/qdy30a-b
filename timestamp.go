package main

import (
	"fmt"
	"io"
	"time"
)

const timestampLayout = "2006-01-02 15:04:05.000000"

func formatTimestamp(timestamp time.Time) string {
	return timestamp.Local().Truncate(time.Microsecond).Format(timestampLayout)
}

func writeTimestamped(w io.Writer, timestamp time.Time, format string, args ...any) error {
	values := make([]any, 0, len(args)+1)
	values = append(values, formatTimestamp(timestamp))
	values = append(values, args...)
	_, err := fmt.Fprintf(w, "%s "+format, values...)
	return err
}

type timestampWriter struct {
	output io.Writer
}

func (w timestampWriter) Write(data []byte) (int, error) {
	if err := writeTimestamped(w.output, time.Now(), "%s", data); err != nil {
		return 0, err
	}
	return len(data), nil
}
