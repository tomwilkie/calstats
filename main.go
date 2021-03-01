package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"

	calv3 "google.golang.org/api/calendar/v3"

	"github.com/tomwilkie/calstats/calendar"
)

var verbose bool
var ignoreRegexps []*regexp.Regexp

func main() {
	var ignorelist string
	flag.BoolVar(&verbose, "v", false, "")
	flag.StringVar(&ignorelist, "ignorelist", "ignorelist", "")
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

	if err := writer.Write([]string{"email", "tz", "free slots", "meeting hours", "% meetings"}); err != nil {
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
		r, err := regexp.Compile("^" + scanner.Text() + "$")
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

	slots, start, end, err := workingSlots(7, cal.TimeZone)
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

	for i := 0; i < len(slots); i++ {
		if verbose {
			fmt.Println(slots[i].summary)
		}

		var meetingFound bool
	next:
		for j := 0; j < len(events.Items); j++ {
			var eventStart time.Time
			var err error

			if events.Items[j].OriginalStartTime != nil {
				eventStart, err = time.Parse(time.RFC3339, events.Items[j].OriginalStartTime.DateTime)
			} else {
				eventStart, err = time.Parse(time.RFC3339, events.Items[j].Start.DateTime)
			}
			if err != nil {
				continue next
			} else if eventStart.After(slots[i].end) || eventStart.Equal(slots[i].end) {
				//fmt.Println(events.Items[j].Summary, eventStart, ">", slots[i].end)
				continue next
			}

			originalStart, err := time.Parse(time.RFC3339, events.Items[j].Start.DateTime)
			originalEnd, err := time.Parse(time.RFC3339, events.Items[j].End.DateTime)
			duration := originalEnd.Sub(originalStart)

			eventEnd := eventStart.Add(duration)
			if err != nil {
				continue next
			} else if eventEnd.Before(slots[i].start) || eventEnd.Equal(slots[i].start) {
				//fmt.Println("\t", events.Items[j].Summary, eventEnd, "<", slots[i].start)
				continue next
			}

			if ignoreEvent(id, events.Items[j]) {
				if verbose {
					fmt.Printf("\t%v (IGNORED %v->%v)\n", events.Items[j].Summary, eventStart, eventEnd)
				}
				continue next
			}

			if verbose {
				fmt.Printf("\t%v (%v->%v)\n", events.Items[j].Summary, eventStart, eventEnd)
			}
			meetingFound = true
			totalMeetings += duration
		}
		if !meetingFound {
			freeSlots++
		}
	}

	if err := writer.Write([]string{
		id, cal.TimeZone, strconv.Itoa(freeSlots),
		fmt.Sprintf("%0.1f", totalMeetings.Hours()),
		fmt.Sprintf("%0.0d%%", totalMeetings*100/(40*time.Hour)),
	}); err != nil {
		return err
	}

	return nil
}

func ignoreEvent(email string, event *calv3.Event) bool {
	// We can skip some events based on name.
	for _, r := range ignoreRegexps {
		if r.MatchString(event.Summary) {
			return true
		}
	}

	// We should ignore events the user has explicity not accepted.
	for _, attendee := range event.Attendees {
		if attendee.Email == email && attendee.ResponseStatus == "declined" {
			return true
		}
	}

	return false
}

type slot struct {
	summary    string
	start, end time.Time
}

func workingSlots(days int, tz string) ([]slot, time.Time, time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	// We assume people work 7am - 7pm in their local timezone.
	start, err := time.ParseInLocation("15:04:05", "07:00:00", loc)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	yy, mm, dd := time.Now().Date()
	start = start.AddDate(yy, int(mm)-1, dd-1)
	end := start

	result := []slot{}
	for i := 0; i < days; i++ {
		if end.Weekday() == time.Saturday || end.Weekday() == time.Sunday {
			end = end.Add(24 * time.Hour)
			continue
		}

		result = append(result,
			slot{
				summary: fmt.Sprintf("%s Morning", end.Format("Mon Jan 2")),
				start:   end,
				end:     end.Add(6 * time.Hour),
			},
			slot{
				summary: fmt.Sprintf("%s Afternoon", end.Format("Mon Jan 2")),
				start:   end.Add(6 * time.Hour),
				end:     end.Add(12 * time.Hour),
			},
		)
		end = end.Add(24 * time.Hour)
	}

	return result, start, end, nil
}
