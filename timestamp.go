package main

import "time"

const timestampLayout = "2006-01-02 15:04:05.000000"

func formatTimestamp(timestamp time.Time) string {
	return timestamp.Local().Truncate(time.Microsecond).Format(timestampLayout)
}
