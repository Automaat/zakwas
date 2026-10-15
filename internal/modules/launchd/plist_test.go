package launchd

import (
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func TestRender(t *testing.T) {
	paths := config.Paths{Home: "/Users/me", Root: "/repo"}
	tests := []struct {
		name string
		job  config.LaunchdJob
		want []string
		not  []string
	}{
		{
			name: "daily at a time",
			job:  job("com.example.daily"),
			want: []string{
				"<key>Label</key>\n\t<string>com.example.daily</string>",
				"<array>\n\t\t<string>/Users/me/.local/bin/zakwas-check-notify</string>\n\t</array>",
				"<key>StartCalendarInterval</key>\n\t<dict>\n\t\t<key>Hour</key>\n\t\t<integer>10</integer>\n\t\t<key>Minute</key>\n\t\t<integer>0</integer>\n\t</dict>",
			},
			not: []string{"RunAtLoad", "StartInterval", "EnvironmentVariables", "StandardOutPath", "Weekday"},
		},
		{
			name: "weekdays are sorted and use launchd numbering",
			job: config.LaunchdJob{
				Label: "com.example.weekly", Program: []string{"/bin/true"},
				Schedule: config.LaunchdSchedule{At: "07:30", Weekdays: []string{"fri", "mon", "sun"}},
			},
			want: []string{
				"<key>Weekday</key>\n\t\t\t<integer>0</integer>",
				"<key>Weekday</key>\n\t\t\t<integer>1</integer>",
				"<key>Weekday</key>\n\t\t\t<integer>5</integer>",
				"<key>Hour</key>\n\t\t\t<integer>7</integer>",
				"<key>Minute</key>\n\t\t\t<integer>30</integer>",
			},
		},
		{
			name: "interval, run at load, env, log",
			job: config.LaunchdJob{
				Label: "com.example.poll", Program: []string{"/bin/sh", "-c", "echo 'a & b' < ~/in"},
				Schedule:  config.LaunchdSchedule{Every: "90m"},
				RunAtLoad: true,
				Env:       map[string]string{"PATH": "/usr/bin:/bin", "A": "x"},
				Log:       "~/Library/Logs/poll.log",
			},
			want: []string{
				"<key>StartInterval</key>\n\t<integer>5400</integer>",
				"<key>RunAtLoad</key>\n\t<true/>",
				"<string>echo &#39;a &amp; b&#39; &lt; ~/in</string>",
				"<key>A</key>\n\t\t<string>x</string>\n\t\t<key>PATH</key>",
				"<key>StandardOutPath</key>\n\t<string>/Users/me/Library/Logs/poll.log</string>",
				"<key>StandardErrorPath</key>\n\t<string>/Users/me/Library/Logs/poll.log</string>",
			},
			not: []string{"StartCalendarInterval"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := render(tt.job, paths)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(got), w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(string(got), n) {
					t.Errorf("unexpected %q in\n%s", n, got)
				}
			}
			again, _ := render(tt.job, paths)
			if string(again) != string(got) {
				t.Error("render is not deterministic")
			}
		})
	}
}
