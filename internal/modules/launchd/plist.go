package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
)

const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

// render builds the job's plist. The output is deterministic, so comparing
// it with the installed file detects every change to the job.
func render(j config.LaunchdJob, paths config.Paths) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(plistHeader)
	put := func(indent int, format string, args ...any) {
		b.WriteString(strings.Repeat("\t", indent))
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}
	str := func(indent int, s string) { put(indent, "<string>%s</string>", escape(s)) }

	put(1, "<key>Label</key>")
	str(1, j.Label)

	put(1, "<key>ProgramArguments</key>")
	put(1, "<array>")
	for i, a := range j.Program {
		if i == 0 {
			a = paths.Dst(a)
		}
		str(2, a)
	}
	put(1, "</array>")

	if j.RunAtLoad {
		put(1, "<key>RunAtLoad</key>")
		put(1, "<true/>")
	}

	if err := schedule(j.Schedule, put); err != nil {
		return nil, err
	}

	if len(j.Env) > 0 {
		put(1, "<key>EnvironmentVariables</key>")
		put(1, "<dict>")
		keys := make([]string, 0, len(j.Env))
		for k := range j.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			put(2, "<key>%s</key>", escape(k))
			str(2, j.Env[k])
		}
		put(1, "</dict>")
	}

	if j.Log != "" {
		log := paths.Dst(j.Log)
		for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
			put(1, "<key>%s</key>", key)
			str(1, log)
		}
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}

func schedule(s config.LaunchdSchedule, put func(int, string, ...any)) error {
	if s.Every != "" {
		d, err := s.Interval()
		if err != nil {
			return err
		}
		put(1, "<key>StartInterval</key>")
		put(1, "<integer>%d</integer>", int(d.Seconds()))
		return nil
	}
	hour, minute, err := config.LaunchdAt(s.At)
	if err != nil {
		return err
	}
	days := make([]int, 0, len(s.Weekdays))
	for _, name := range s.Weekdays {
		n, ok := config.LaunchdWeekday(name)
		if !ok {
			return fmt.Errorf("weekday %q is not one of mon, tue, wed, thu, fri, sat, sun", name)
		}
		days = append(days, n)
	}
	slices.Sort(days)
	days = slices.Compact(days)

	entry := func(indent int, weekday int) {
		put(indent, "<dict>")
		if weekday >= 0 {
			put(indent+1, "<key>Weekday</key>")
			put(indent+1, "<integer>%d</integer>", weekday)
		}
		put(indent+1, "<key>Hour</key>")
		put(indent+1, "<integer>%d</integer>", hour)
		put(indent+1, "<key>Minute</key>")
		put(indent+1, "<integer>%d</integer>", minute)
		put(indent, "</dict>")
	}
	put(1, "<key>StartCalendarInterval</key>")
	if len(days) == 0 {
		entry(1, -1)
		return nil
	}
	put(1, "<array>")
	for _, d := range days {
		entry(2, d)
	}
	put(1, "</array>")
	return nil
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
