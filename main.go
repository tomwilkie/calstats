package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	calv3 "google.golang.org/api/calendar/v3"

	"github.com/tomwilkie/calstats/calendar"
)

var verbose bool
var ignoreRegexps []*regexp.Regexp
var start string
var duration int

const (
	personal    = "personal"
	ignore      = "ignored"
	declined    = "declined"
	notAccepted = "not accepted"
	hiring      = "hiring"
	meeting     = "meeting"
)

var categories = []string{personal, ignore, declined, notAccepted, hiring, meeting}
var count = []string{hiring, meeting}

func main() {
	var ignorelist string
	flag.BoolVar(&verbose, "v", false, "")
	flag.StringVar(&ignorelist, "ignorelist", "ignorelist", "")
	flag.StringVar(&start, "start", time.Now().Format("2006/01/02")+" 07:00:00", "")
	flag.IntVar(&duration, "duration", 24*7, "hours")
	flag.Parse()

	// Load & compile ignore regexps.
	var err error
	ignoreRegexps, err = loadIgnores(ignorelist)
	if err != nil {
		log.Fatalf("Unable to parse ignore list: %v", err)
	}

	srv, err := calendar.Connect()
	if err != nil {
		log.Fatalf("Unable to retrieve Calendar client: %v", err)
	}

	writer := csv.NewWriter(os.Stdout)
	defer writer.Flush()

	columns := []string{"email", "tz", "half days free"}
	columns = append(columns, categories...)
	columns = append(columns, "meeting hours", "% meetings")
	if err := writer.Write(columns); err != nil {
		log.Fatalf("Error writing CSV: %v", err)
	}

	for _, id := range flag.Args() {
		if err := processCalendar(srv, id, writer); err != nil {
			log.Fatalf("Error processing calendar: %v", err)
		}
	}
}

func loadIgnores(filename string) ([]*regexp.Regexp, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var result []*regexp.Regexp
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}

		r, err := regexp.Compile("^" + line + "$")
		if err != nil {
			return nil, err
		}

		result = append(result, r)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func processCalendar(srv *calv3.Service, id string, writer *csv.Writer) error {
	defer writer.Flush()

	cal, err := srv.Calendars.Get(id).Do()
	if err != nil {
		return err
	}

	slots, start, end, err := workingSlots(cal.TimeZone)
	if err != nil {
		return err
	}

	events, err := srv.Events.List(id).ShowDeleted(false).
		SingleEvents(true).TimeMin(start.Format(time.RFC3339)).
		TimeMax(end.Format(time.RFC3339)).
		OrderBy("startTime").Do()
	if err != nil {
		return err
	}

	var freeSlots int
	var totalMeetings time.Duration
	totals := map[string]time.Duration{}

	for _, slot := range slots {
		if verbose {
			fmt.Printf("%s (%s -> %s)\n", slot.summary, slot.start.Format("15:04:05"), slot.end.Format("15:04:05"))
		}

		var meetingFound bool
	next:
		for _, event := range events.Items {
			// Ignore all day-events.
			if event.Start.DateTime == "" {
				continue
			}

			start, end, err := parseStartEnd(event)
			if err != nil {
				return err
			}

			if !(start.Before(slot.end) && end.After(slot.start)) {
				continue next
			}

			category := categorise(id, event)
			duration := end.Sub(start)
			totals[category] += duration
			if verbose {
				fmt.Printf("\t%v [%s]: %s (%0.0fmins)\n", start.Format("15:04:05"), category, event.Summary, duration.Minutes())
			}

			if i := sort.SearchStrings(count, category); i < len(count) && count[i] == category {
				totalMeetings += duration
				meetingFound = true
			}
		}
		if !meetingFound {
			freeSlots++
		}
	}

	columns := []string{id, cal.TimeZone, strconv.Itoa(freeSlots)}
	for _, c := range categories {
		columns = append(columns, fmt.Sprintf("%0.1f", totals[c].Hours()))
	}
	columns = append(columns, fmt.Sprintf("%0.1f", totalMeetings.Hours()), fmt.Sprintf("%0.0d%%", totalMeetings*100/(40*time.Hour)))

	if err := writer.Write(columns); err != nil {
		return err
	}

	return nil
}

func parseStartEnd(event *calv3.Event) (start time.Time, end time.Time, err error) {
	// Calendars are... hard.
	// We have 2 starts, and 1 end:
	// - Start: The (inclusive) start time of the event. For a recurring
	//   event, this is the start time of the first instance.
	// - End: The (exclusive) end time of the event. For a recurring event,
	//   this is the end time of the first instance.
	// - OriginalStartTime: For an instance of a recurring event, this is the
	//   time at which this event would start according to the recurrence data
	//   in the recurring event identified by recurringEventId. It uniquely
	//   identifies the instance within the recurring event series even if the
	//   instance was moved to a different time. Immutable.
	//
	// There seems to be no "OriginalEndTime".  Or Event duration.
	// However, sometimes I've found OriginalStartTime < Start - WTF?

	start, err = time.Parse(time.RFC3339, event.Start.DateTime)
	if err != nil {
		return
	}

	var originalStart time.Time
	if event.OriginalStartTime != nil {
		originalStart, err = time.Parse(time.RFC3339, event.Start.DateTime)
		if err != nil {
			return
		}

		if originalStart.After(start) {
			start = originalStart
		}
	}

	end, err = time.Parse(time.RFC3339, event.End.DateTime)
	if err != nil {
		return
	}

	return
	//	duration := originalEnd.Sub(originalStart)
	//	end := eventStart.Add(duration)
}

func categorise(email string, event *calv3.Event) (reason string) {
	if strings.Contains(event.Description, "https://hire.lever.co/interviews") {
		return "hiring"
	}

	// Ignore events with only the owner as te attendee, created
	// by the owner.
	if event.Creator != nil && event.Creator.Self {
		if len(event.Attendees) == 0 {
			return "personal"
		}
		if len(event.Attendees) == 1 && event.Attendees[0].Email == email {
			return "personal"
		}
	}

	// We can skip some events based on name.
	for _, r := range ignoreRegexps {
		if r.MatchString(event.Summary) {
			return "ignored"
		}
	}

	// We should ignore events the user has explicity not accepted.
	for _, attendee := range event.Attendees {
		if attendee.Email != email {
			continue
		}
		if attendee.ResponseStatus == "declined" {
			return "declined"
		}
		if attendee.ResponseStatus != "accepted" {
			return "not accepted"
		}
	}

	return "meeting"
}

type slot struct {
	summary    string
	start, end time.Time
}

func workingSlots(tz string) ([]slot, time.Time, time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	// We assume people work 7am - 7pm in their local timezone.
	start, err := time.ParseInLocation("2006/01/02 15:04:05", start, loc)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	end := start.Add(time.Duration(duration) * time.Hour)
	result := []slot{}
	for curr := start; curr.Before(end); curr = curr.Add(24 * time.Hour) {
		if curr.Weekday() == time.Saturday || curr.Weekday() == time.Sunday {
			continue
		}

		result = append(result,
			slot{
				summary: fmt.Sprintf("%s Morning", curr.Format("Mon Jan 2")),
				start:   curr,
				end:     curr.Add(6 * time.Hour),
			},
			slot{
				summary: fmt.Sprintf("%s Afternoon", curr.Format("Mon Jan 2")),
				start:   curr.Add(6 * time.Hour),
				end:     curr.Add(12 * time.Hour),
			},
		)
	}

	return result, start, end, nil
}
