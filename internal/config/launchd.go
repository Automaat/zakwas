package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// LaunchAgentsDir is where zakwas writes launchd user agent plists.
const LaunchAgentsDir = "~/Library/LaunchAgents"

// MinLaunchdInterval is the shortest schedule.every, so a typo can't start a
// job in a tight loop.
const MinLaunchdInterval = time.Minute

// Launchd weekday names in launchd's numbering (0 is Sunday).
var launchdWeekdays = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

var (
	launchdLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	launchdAt    = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
)

// LaunchdJob is one launchd user agent zakwas installs and loads.
type LaunchdJob struct {
	Label     string            `yaml:"label" jsonschema:"required,minLength=1,pattern=^[A-Za-z0-9][A-Za-z0-9._-]*$" jsonschema_description:"Reverse-DNS style job name, e.g. 'com.example.drift-check'. Letters, digits, '.', '_' and '-'. The plist is written to ~/Library/LaunchAgents/<label>.plist. zakwas only unloads or deletes jobs it created, so a label already loaded from another plist is an error."`
	Program   []string          `yaml:"program" jsonschema:"required,minItems=1" jsonschema_description:"Program and arguments (launchd ProgramArguments). The first entry must be an absolute path or start with '~/'. launchd starts jobs from '/' with a minimal PATH and does not expand '$VAR' or '~', so call a script or '/bin/sh -c' and set 'env' as needed."`
	Schedule  LaunchdSchedule   `yaml:"schedule" jsonschema:"required" jsonschema_description:"When the job runs: set exactly one of 'every' and 'at'."`
	RunAtLoad bool              `yaml:"runAtLoad" jsonschema:"default=false" jsonschema_description:"Also run the job once whenever it is loaded: at login and after every change to the job. Default false."`
	Env       map[string]string `yaml:"env" jsonschema_description:"Environment variables for the job, e.g. PATH. Values are written as is, with no expansion."`
	Log       string            `yaml:"log" jsonschema_description:"File receiving the job's stdout and stderr: absolute or starting with '~/'. launchd creates the file but not its directory. Default: output is discarded."`
}

// LaunchdSchedule says when a job runs.
type LaunchdSchedule struct {
	Every    string   `yaml:"every" jsonschema_description:"Run at this interval, a Go duration of at least 1m such as '30m' or '6h'. A Mac that was asleep runs the job once on wake, not once per missed interval."`
	At       string   `yaml:"at" jsonschema:"pattern=^([01][0-9]|2[0-3]):[0-5][0-9]$" jsonschema_description:"Run at this local time of day, 'HH:MM' on a 24-hour clock. A Mac that was asleep runs the job on wake."`
	Weekdays []string `yaml:"weekdays" jsonschema:"minItems=1,uniqueItems=true,enum=mon,enum=tue,enum=wed,enum=thu,enum=fri,enum=sat,enum=sun" jsonschema_description:"Limit 'at' to these days: mon, tue, wed, thu, fri, sat, sun. Default: every day."`
}

// ValidLaunchdLabel reports whether label is safe as a plist file name.
func ValidLaunchdLabel(label string) bool { return launchdLabel.MatchString(label) }

// LaunchdAt splits a validated "HH:MM" into hour and minute.
func LaunchdAt(at string) (hour, minute int, err error) {
	m := launchdAt.FindStringSubmatch(at)
	if m == nil {
		return 0, 0, fmt.Errorf("at %q is not HH:MM", at)
	}
	if hour, err = strconv.Atoi(m[1]); err != nil {
		return 0, 0, err
	}
	minute, err = strconv.Atoi(m[2])
	return hour, minute, err
}

// LaunchdWeekday maps a weekday name to launchd's number (0 is Sunday).
func LaunchdWeekday(name string) (int, bool) {
	n, ok := launchdWeekdays[name]
	return n, ok
}

// Interval parses schedule.every.
func (s LaunchdSchedule) Interval() (time.Duration, error) {
	d, err := time.ParseDuration(s.Every)
	switch {
	case err != nil:
		return 0, fmt.Errorf("every %q: %w", s.Every, err)
	case d < MinLaunchdInterval:
		return 0, fmt.Errorf("every %q must be at least %s", s.Every, MinLaunchdInterval)
	case d%time.Second != 0:
		return 0, fmt.Errorf("every %q must be a whole number of seconds", s.Every)
	}
	return d, nil
}

// PlistDst is where the job's plist lives, as a '~/' path.
func (j LaunchdJob) PlistDst() string { return LaunchAgentsDir + "/" + j.Label + ".plist" }

func (c *Config) validateLaunchd(paths Paths, claimDst func(dst, what string)) []error {
	var errs []error
	seen := map[string]int{}
	for i, j := range c.Launchd {
		what := fmt.Sprintf("launchd[%d]", i)
		switch {
		case j.Label == "":
			errs = append(errs, fmt.Errorf("%s: label is required", what))
		case !ValidLaunchdLabel(j.Label):
			errs = append(errs, fmt.Errorf("%s: label %q may only hold letters, digits, '.', '_' and '-', and must start with a letter or digit", what, j.Label))
		default:
			if first, dup := seen[strings.ToLower(j.Label)]; dup {
				errs = append(errs, fmt.Errorf("%s: label %q is already used by launchd[%d]", what, j.Label, first))
			} else {
				seen[strings.ToLower(j.Label)] = i
				claimDst(j.PlistDst(), what)
			}
		}
		errs = append(errs, j.validate(paths, what)...)
	}
	return errs
}

func (j LaunchdJob) validate(paths Paths, what string) []error {
	var errs []error
	switch {
	case len(j.Program) == 0 || j.Program[0] == "":
		errs = append(errs, fmt.Errorf("%s: program is required", what))
	case strings.Contains(j.Program[0], "$"):
		errs = append(errs, fmt.Errorf("%s: program %q: environment variables are not expanded, use ~/", what, j.Program[0]))
	case !isAbs(paths, j.Program[0]):
		errs = append(errs, fmt.Errorf("%s: program %q must be an absolute path or start with ~/", what, j.Program[0]))
	}
	if j.Log != "" {
		if err := checkDst(paths, j.Log); err != nil {
			errs = append(errs, fmt.Errorf("%s: log: %w", what, err))
		}
	}
	for k := range j.Env {
		if k == "" || strings.Contains(k, "=") {
			errs = append(errs, fmt.Errorf("%s: env name %q must be non-empty and contain no '='", what, k))
		}
	}
	errs = append(errs, j.checkText(what)...)
	return append(errs, j.Schedule.validate(what)...)
}

// checkText rejects control characters in the values written into the plist:
// XML cannot hold them, so the escaper would silently replace them.
func (j LaunchdJob) checkText(what string) []error {
	var errs []error
	check := func(field, value string) {
		if i := strings.IndexFunc(value, plistUnsafe); i >= 0 {
			errs = append(errs, fmt.Errorf("%s: %s %q contains a control character", what, field, value))
		}
	}
	for i, a := range j.Program {
		check(fmt.Sprintf("program[%d]", i), a)
	}
	check("log", j.Log)
	for k, v := range j.Env {
		check("env name", k)
		check("env "+k, v)
	}
	return errs
}

func plistUnsafe(r rune) bool {
	return unicode.IsControl(r) && r != '\t' && r != '\n'
}

func (s LaunchdSchedule) validate(what string) []error {
	var errs []error
	switch {
	case s.Every == "" && s.At == "":
		return []error{fmt.Errorf("%s: schedule needs 'every' or 'at'", what)}
	case s.Every != "" && s.At != "":
		return []error{fmt.Errorf("%s: schedule sets both 'every' and 'at'; use one", what)}
	}
	if s.Every != "" {
		if _, err := s.Interval(); err != nil {
			errs = append(errs, fmt.Errorf("%s: schedule: %w", what, err))
		}
		if len(s.Weekdays) > 0 {
			errs = append(errs, fmt.Errorf("%s: schedule.weekdays only works with 'at'", what))
		}
	}
	if s.At != "" && !launchdAt.MatchString(s.At) {
		errs = append(errs, fmt.Errorf("%s: schedule.at %q is not HH:MM on a 24-hour clock", what, s.At))
	}
	if s.Weekdays != nil && len(s.Weekdays) == 0 {
		errs = append(errs, fmt.Errorf("%s: schedule.weekdays is empty; list at least one day or omit it", what))
	}
	var days []string
	for _, d := range s.Weekdays {
		if _, ok := launchdWeekdays[d]; !ok {
			errs = append(errs, fmt.Errorf("%s: schedule.weekdays: %q is not one of mon, tue, wed, thu, fri, sat, sun", what, d))
		} else if slices.Contains(days, d) {
			errs = append(errs, fmt.Errorf("%s: schedule.weekdays: %q is listed twice", what, d))
		}
		days = append(days, d)
	}
	return errs
}

func isAbs(paths Paths, p string) bool {
	return strings.HasPrefix(paths.Dst(p), "/")
}
