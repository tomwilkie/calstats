# Google Calendar Analysis Tool

This tool is there to analyse the number of hours the team spend in meetings, and how many "slots" (mornings or afternoons in their local TZ) the team have calls - the idea being that even with a low % of meetings, one 30min call in the middle of morning can disrupt the whole "slot".

To use:
1. `git clone https://github.com/tomwilkie/calstats`
1. Go to [Google's Go Quickstart](https://developers.google.com/calendar/quickstart/go) and click "Enable the Google Calendar API".  Save the `credentials.json` file to this directory.
1. Run `go run main.go -v tom@grafana.com` and behold.
