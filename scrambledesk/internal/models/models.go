package models

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phpdave11/gofpdf"
	"scrambledesk-client/config"
	"scrambledesk-client/internal/pdf"
)

type Competition struct {
	ID      string
	Name    string
	Rounds  []Round
	Persons []Person
}

type Round struct {
	ID           int
	EventName    string
	ActivityCode string
	EventId      string
	RoundNumber  int
	GroupCount   int
	Groups       []Group
	Finished     bool
	Results      []Result
	StartTime    time.Time
	EndTime      time.Time
}

type Result struct {
	PersonId int
	Ranking  int
}

type Group struct {
	ActivityId      int    `json:"id"`
	ActivityCode    string `json:"activityCode"`
	EventName       string `json:"name"`
	EventId         string
	StartTime       time.Time
	EndTime         time.Time
	RoundNumber     int
	GroupNumber     int
	Opened          bool
	OpenedTimestamp []time.Time
	ClosedTimestamp []time.Time
	Finished        bool
	Competitors     []Person
	Staff           []Person
	Password        string
}

type Person struct {
	ID          int      `json:"registrantId"`
	Name        string   `json:"name"`
	WcaId       string   `json:"wcaId"`
	Roles       []string `json:"roles"`
	Avatar      Avatar
	Assignments []Assignment
}

type Assignment struct {
	ActivityId     int
	StationNumber  int
	AssignmentCode string
}

type Avatar struct {
	Url      string
	ThumbUrl string
}

func (r *Round) Competitors() []Person {
	var competitors []Person
	for _, g := range r.Groups {
		competitors = append(competitors, g.Competitors...)
	}
	return competitors
}

func (c *Competition) OpenScrambleSet(activityCode string) error {
	for i, r := range c.Rounds {
		for j, g := range r.Groups {
			if g.ActivityCode == activityCode {
				// NOTE: We call SendPDF() on the real group as this modifies the underlying data
				err := c.Rounds[i].Groups[j].SendPDF(c.Name)
				if err != nil {
					return err
				}
				return nil
			}
		}
	}
	return errors.New("scramble set not found")
}

func (c *Competition) StartFrom(activityCode string) error {
	c.SortRounds()
	for i, r := range c.Rounds {
		for j := range r.Groups {
			c.Rounds[i].Groups[j].Finished = false
		}
		c.Rounds[i].Finished = false
	}

	for i, r := range c.Rounds {
		for j, g := range r.Groups {
			if g.ActivityCode == activityCode {
				return nil
			}
			c.Rounds[i].Groups[j].Finished = true
		}
		c.Rounds[i].Finished = true
	}
	return errors.New("scramble set not found")
}

func (c *Competition) AssignCompetitors() {
	for i := range c.Rounds {
		for j := range c.Rounds[i].Groups {
			group := &c.Rounds[i].Groups[j]

			for _, person := range c.Persons {
				for _, assign := range person.Assignments {
					if assign.ActivityId == group.ActivityId {
						simplified := Person{
							ID:    person.ID,
							Name:  person.Name,
							WcaId: person.WcaId,
							Roles: person.Roles,
						}

						if assign.AssignmentCode == "competitor" {
							group.Competitors = append(group.Competitors, simplified)
						}
					}
				}
			}
		}
	}
}

func (c *Competition) NextGroup() *Group {
	for i, r := range c.Rounds {
		if r.Finished {
			continue
		}
		for j, g := range r.Groups {
			if !g.Finished {
				return &c.Rounds[i].Groups[j]
			}
		}
	}
	return nil
}

func (c *Competition) prevRound(r Round) *Round {
	for i, cr := range c.Rounds {
		if cr.EventId != r.EventId {
			continue
		}
		if cr.RoundNumber == r.RoundNumber-1 {
			return &c.Rounds[i]
		}
	}
	return nil
}

func (c *Competition) LoadResults() {
	var raw struct {
		Events []wcifEvent `json:"events"`
	}

	data, err := FetchWCIF(c.ID)
	if err != nil {
		log.Fatal(err)
	}

	err = json.Unmarshal(data, &raw)
	if err != nil {
		log.Fatalf("Could not unmarshal data: %v", err)
	}

	resultMap := make(map[string][]Result)

	for _, e := range raw.Events {
		for _, r := range e.Rounds {
			resultMap[r.ID] = r.Results
		}
	}

	for i, r := range c.Rounds {
		c.Rounds[i].Results = resultMap[r.ActivityCode]
	}

}

func (c *Competition) AssignAdvancedRoundCompetitors() {
	c.LoadResults()
	for i, r := range c.Rounds {
		// Skip initial rounds
		if r.RoundNumber < 2 {
			continue
		}

		// Skip if no results are posted
		if len(r.Results) == 0 {
			continue
		}

		competitorMap := make(map[int]Person)
		for _, p := range c.Persons {
			competitorMap[p.ID] = p
		}

		var competitors []Person

		// TODO: Let's not hard code the advancement conditions, no?
		var results []Result
		allResults := c.prevRound(r).Results
		sort.Slice(allResults, func(i, j int) bool {
			return allResults[i].Ranking < allResults[j].Ranking
		})
		roundCompetitorCount := int(float64(len(allResults)) * 0.75)
		for i := range roundCompetitorCount {
			results = append(results, allResults[i])
		}
		sort.Slice(results, func(i, j int) bool {
			return results[i].Ranking > results[j].Ranking
		})
		for _, r := range results {
			competitors = append(competitors, competitorMap[r.PersonId])
		}

		competitorCount := len(competitors)
		baseGroupSize := competitorCount / r.GroupCount
		extras := competitorCount % r.GroupCount

		start := 0
		for j := range r.GroupCount {
			groupSize := baseGroupSize
			if j < extras {
				groupSize++
			}
			end := start + groupSize
			groupCompetitors := competitors[start:end]

			// Order competitors by name
			sort.Slice(groupCompetitors, func(i, j int) bool {
				return groupCompetitors[i].Name < groupCompetitors[j].Name
			})
			c.Rounds[i].Groups[j].Competitors = groupCompetitors
			start = end
		}
	}
	c.AssignStaff()
}

func (c *Competition) AssignStaff() {
	for i, r := range c.Rounds {
		// Empty the groups before populating them
		for j := range r.Groups {
			c.Rounds[i].Groups[j].Staff = []Person{}
		}

		totalGroups := len(r.Groups)
		if totalGroups == 1 {
			continue
		}

		for j := range r.Groups {
			staffRound := (j - 1 + totalGroups) % totalGroups
			c.Rounds[i].Groups[j].Staff = r.Groups[staffRound].Competitors
		}
	}
}

func (c *Competition) loadAdvancedRoundData() {
	scrambleDir := fmt.Sprintf("./%s - Computer Display PDFs", c.Name)
	scrambleSets, err := os.ReadDir(scrambleDir)
	if err != nil {
		log.Fatalf("Could not read scramble sets: %v", err)
	}

	for i, r := range c.Rounds {
		// Skip initial rounds
		if r.RoundNumber == 1 {
			continue
		}

		var roundScrambleSets []string
		for _, s := range scrambleSets {
			eventName := strings.ReplaceAll(r.EventName, ",", "")
			eventName = strings.ReplaceAll(eventName, " Cube", "")
			if strings.HasPrefix(s.Name(), eventName) {
				roundScrambleSets = append(roundScrambleSets, s.Name())
			}
		}

		// Assign group count to the round
		c.Rounds[i].GroupCount = len(roundScrambleSets)

		for j, s := range roundScrambleSets {

			if j < len(r.Groups) {
				continue
			}

			roundName := c.Rounds[i].EventName
			roundNumber := int(roundName[len(roundName)-1]) - 48
			groupNumber := j + 1
			eventName := strings.Split(s, " Scramble")[0]
			activityCode := fmt.Sprintf("%s-g%d", r.ActivityCode, j+1)

			group := Group{
				EventName:    eventName,
				RoundNumber:  roundNumber,
				GroupNumber:  groupNumber,
				Opened:       false,
				Finished:     false,
				ActivityCode: activityCode,
				// TODO Calculate this based of how many groups the round has
				StartTime: r.StartTime,
				EndTime:   r.EndTime,
			}
			c.Rounds[i].Groups = append(c.Rounds[i].Groups, group)
		}
	}
}

func (c *Competition) loadInitialRoundData() {
	scrambleDir := fmt.Sprintf("./%s - Computer Display PDFs/", c.Name)
	scrambleSets, err := os.ReadDir(scrambleDir)
	if err != nil {
		log.Fatalf("Could not read scramble sets: %v", err)
	}

	for i, r := range c.Rounds {
		// Skip advanced rounds
		if r.RoundNumber != 1 {
			continue
		}

		var roundScrambleSets []string
		for _, s := range scrambleSets {
			eventName := strings.ReplaceAll(r.EventName, ",", "")
			eventName = strings.ReplaceAll(eventName, " Cube", "")
			if strings.HasPrefix(s.Name(), eventName) {
				roundScrambleSets = append(roundScrambleSets, s.Name())
			}
		}

		// Assign group count to the round
		c.Rounds[i].GroupCount = len(roundScrambleSets)

		for j, s := range roundScrambleSets {

			roundNumber := i + 1
			groupNumber := j + 1
			eventName := strings.Split(s, " Scramble")[0]
			activityCode := fmt.Sprintf("%s-g%d", r.ActivityCode, j+1)

			group := Group{
				EventName:    eventName,
				RoundNumber:  roundNumber,
				GroupNumber:  groupNumber,
				Opened:       false,
				Finished:     false,
				ActivityCode: activityCode,
				// TODO Calculate this based of how many groups the round has
				StartTime: r.StartTime,
				EndTime:   r.EndTime,
			}
			c.Rounds[i].Groups = append(c.Rounds[i].Groups, group)
		}
	}
}

func (c *Competition) groupsLoaded() bool {
	for _, r := range c.Rounds {
		// Ignore advanced rounds
		if r.RoundNumber != 1 {
			continue
		}

		return len(r.Groups) > 0
	}
	return false
}

func (c *Competition) LoadRoundData() {
	for i, r := range c.Rounds {
		roundNumber := int(r.EventName[len(r.EventName)-1]) - 48
		c.Rounds[i].RoundNumber = roundNumber
		c.Rounds[i].GroupCount = len(r.Groups)
		c.Rounds[i].SortGroups()
		for j := range r.Groups {
			activityCode := fmt.Sprintf("%s-g%d", r.ActivityCode, j+1)
			c.Rounds[i].Groups[j].RoundNumber = roundNumber
			c.Rounds[i].Groups[j].EventId = r.EventId
			c.Rounds[i].Groups[j].ActivityCode = activityCode
			c.Rounds[i].Groups[j].GroupNumber = j + 1
		}
	}

	if !c.groupsLoaded() {
		fmt.Println("No initial round groups")
		c.loadInitialRoundData()
	}

	c.loadAdvancedRoundData()
	c.AssignCompetitors()
	c.AssignStaff()
	c.loadPasswords()
}

func (c *Competition) LoadAvatars() error {
	for _, p := range c.Persons {
		err := p.SaveAvatar()
		if err != nil {
			return err
		}
	}

	avatarPath := fmt.Sprintf("%s/avatars", config.AppDataDir)
	ConvertFilesToJPGs(avatarPath)
	return nil
}

func (p *Person) SaveAvatar() error {
	if p.Avatar.Url == "" {
		return nil
	}
	var resp *http.Response
	var err error

	resp, err = http.Get(p.Avatar.ThumbUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		resp, err = http.Get(p.Avatar.Url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("bad status: %s", resp.Status)
		}
	}

	out, err := os.Create(p.ImagePath())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (p *Person) ImagePath() string {
	return fmt.Sprintf("%s/avatars/%s", config.AppDataDir, p.WcaId)
}

// Sort competition rounds by start date from the schedule field
func (c *Competition) SortRounds() {
	sort.Slice(c.Rounds, func(i, j int) bool {
		return c.Rounds[i].StartTime.Before(c.Rounds[j].StartTime)
	})
}

func (r *Round) SortGroups() {
	sort.Slice(r.Groups, func(i, j int) bool {
		return r.Groups[i].ActivityCode < r.Groups[j].ActivityCode
	})
}

func (c *Competition) nextRound() *Round {
	for i, r := range c.Rounds {
		if !r.Finished {
			return &c.Rounds[i]
		}
	}
	return nil
}

func (r *Round) nextGroup() *Group {
	for i, g := range r.Groups {
		if !g.Finished {
			return &r.Groups[i]
		}
	}
	return nil
}

func (c *Competition) StartNextGroup() {
	round := c.nextRound()
	if round == nil {
		fmt.Println("No new rounds")
		return
	}

	group := round.nextGroup()
	if group == nil {
		fmt.Println("No new rounds")
		return
	}

	msg := fmt.Sprintf("Are you sure you want to open %s", group.ScrambleSet())
	yes := confirmationMessage(msg)
	if !yes {
		os.Exit(0)
	}

	if group.StartTime.After(time.Now().Add(15 * time.Minute)) {
		timeToStart := group.StartTime.Sub(time.Now())
		hours := int(timeToStart.Hours())
		minutes := int(timeToStart.Minutes()) % 60

		msg := fmt.Sprintf("\nAre you sure?\nRound is not supposed to start in %dh %dm\n", hours, minutes)
		yes := confirmationMessage(msg)
		if !yes {
			os.Exit(0)
		}
	}

	if round.GroupCount == group.GroupNumber {
		round.Finished = true
	}

	group.Finished = true

	// Ensure we have the competitors for the advanced rounds
	if group.RoundNumber > 1 {
		c.AssignAdvancedRoundCompetitors()
	}
	group.DrawRoundPDF()
	err := curl("profiles.pdf", config.CompetitorListURL)
	if err != nil {
		log.Fatalf("Could not send groups: %v", err)
	}

	err = group.SendPDF(c.Name)
	if err != nil {
		log.Fatalf("Could not send PDF: %v\n", err)
	}
}

func curl(file, url string) error {
	f := fmt.Sprintf("file=@%s", file)
	cmd := exec.Command(
		"curl",
		"--cert", config.ClientCrt,
		"--key", config.ClientKey,
		"--cacert", config.CaCrt,
		"-F", f,
		url,
	)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return nil
}

func (c *Competition) Save() error {
	file, err := os.Create(c.SaveLocation())
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}

// Export the competition with person data removed, for future data analysis
func (c *Competition) Export() error {
	// Remove persons data from the export
	c.Persons = nil
	for i, r := range c.Rounds {
		c.Rounds[i].Results = nil
		for j := range r.Groups {
			c.Rounds[i].Groups[j].Competitors = nil
			c.Rounds[i].Groups[j].Staff = nil
		}
	}
	saveLocation := fmt.Sprintf("%s.json", c.ID)
	exportPath := filepath.Join(config.AppDataDir, "archive", saveLocation)
	file, err := os.Create(exportPath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}

func (c *Competition) SaveLocation() string {
	return fmt.Sprintf("%s/competition.json", config.AppDataDir)
}

func (g *Group) event() string {
	event := strings.ReplaceAll(g.EventName, ",", "")
	event = strings.ReplaceAll(event, " Cube", "")
	event = strings.Split(event, " Group")[0]
	return event
}

func (g Group) ScrambleSet() string {
	groupLetter := string(rune('A' + g.GroupNumber - 1))
	return fmt.Sprintf("%s Scramble Set %s", g.event(), groupLetter)
}

func (g *Group) SendPDF(competition string) error {
	scrambleDir := fmt.Sprintf("./%s - Computer Display PDFs/", competition)
	err := pdf.DecryptPDF(fmt.Sprintf("%s/%s.pdf", scrambleDir, g.ScrambleSet()), g.Password)
	if err != nil {
		return err
	}

	// TODO: Define this on a program level
	scrambleFile := path.Join(config.AppDataDir, "active.pdf")
	err = curl(scrambleFile, config.ScrambleURL)
	if err != nil {
		fmt.Printf("Curl Status Code: %v\n", err)
	}

	g.Opened = true
	g.OpenedTimestamp = append(g.OpenedTimestamp, time.Now())
	return nil
}

func (c *Competition) loadPasswords() error {
	passwordFile := fmt.Sprintf("%s - Computer Display PDF Passcodes - SECRET.txt", c.Name)
	file, err := os.Open(passwordFile)
	if err != nil {
		return err
	}
	defer file.Close()

	passwords := make(map[string]string)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		passwords[key] = value
	}

	err = scanner.Err()
	if err != nil {
		return err
	}

	for i, r := range c.Rounds {
		for j, g := range r.Groups {
			password, ok := passwords[g.ScrambleSet()]
			if ok {
				c.Rounds[i].Groups[j].Password = password
			} else {
				log.Fatalf("Password not found for: %s", g.ScrambleSet())
			}
		}
	}
	return nil
}

func confirmationMessage(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s (y/N): ", prompt)
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("Error reading input: %v\n", err)
		return false
	}

	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

// TODO Change this (DRY)
func (g *Group) DrawHandInPDF() {
	// TODO: Copy placeholder PDF
	if len(g.Competitors) == 0 {
		fmt.Println("Draw hand-in PDF: No competitors found.")
	}
	pdf := gofpdf.NewCustom(&gofpdf.InitType{
		UnitStr: "pt",
		Size: gofpdf.SizeType{
			Wd: 1920,
			Ht: 1080,
		},
	})

	pageMargin := 50.0
	sectionTitleHeight := 40.0
	infoLine := 50.0

	colWidth := (1920 - pageMargin*3) / 2 // Two columns with spacing
	xLeft := pageMargin
	xRight := xLeft + colWidth + pageMargin

	yStart := pageMargin + sectionTitleHeight + 20
	competitorCount := len(g.Competitors) * 2 // Multiply by two for some reason
	if len(g.Staff) > competitorCount/2 {
		competitorCount = len(g.Staff) * 2
	}
	rows := math.Ceil(float64(competitorCount) / 2.0)

	pageHeight := 1080.0
	availableHeight := pageHeight - yStart - pageMargin - infoLine

	lineHeight := availableHeight / rows
	imgSize := lineHeight - 5.0

	pdf.AddPage()

	// Set page color
	pdf.SetTextColor(236, 240, 241)
	pdf.SetFillColor(44, 62, 80)
	pdf.Rect(0, 0, 1920, 1080, "F")

	//    regular := filepath.Join(config.FontDir, "HackNerdFont-Regular.ttf")
	//    bold := filepath.Join(config.FontDir, "HackNerdFont-Bold.ttf")
	// pdf.AddUTF8Font("hack", "", regular)
	// pdf.AddUTF8Font("hack", "B", bold)
	pdf.AddUTF8Font("hack", "", "HackNerdFont-Regular.ttf")
	pdf.AddUTF8Font("hack", "B", "HackNerdFont-Bold.ttf")
	font := "hack"

	// Draw infoline
	infoHeight := pageHeight - 50
	pdf.SetLineWidth(60.0)
	pdf.SetDrawColor(120, 176, 117)
	pdf.Line(0, infoHeight, 1920, infoHeight)

	pdf.SetFont(font, "B", 36)
	pdf.SetXY(infoHeight, 10)
	info := fmt.Sprintf("Preparing %s, please hand in your puzzles!", g.EventName)
	pdf.Text(60, infoHeight+12, info)

	// Define title of staff column based of whether we have assigned staff or not
	var staffTitle string

	if len(g.Staff) > 0 {
		staffTitle = "Staff"
	} else {
		staffTitle = "No staff assigned"
	}

	// Draw section titles
	pdf.SetFont(font, "B", 36)
	pdf.SetXY(xLeft, pageMargin-20)
	pdf.CellFormat(colWidth, sectionTitleHeight, "Competitors", "", 0, "L", false, 0, "")

	pdf.SetXY(xRight, pageMargin-20)
	pdf.CellFormat(colWidth, sectionTitleHeight, staffTitle, "", 0, "L", false, 0, "")

	pdf.SetFont(font, "", 24)

	maxHeight := 1080.0
	rowCount := 0
	i, j := 0, 0

	for {
		// Check if we're out of space
		y := yStart + float64(rowCount)*lineHeight
		if y+lineHeight > maxHeight-pageMargin {
			pdf.AddPage()
			// Redraw section titles
			pdf.SetFont(font, "B", 36)
			pdf.SetXY(xLeft, pageMargin-20)
			pdf.CellFormat(colWidth, sectionTitleHeight, "Competitors", "", 0, "L", false, 0, "")
			pdf.SetFont(font, "", 24)
			rowCount = 0
			y = yStart
		}

		if i < len(g.Competitors) {
			drawPerson(pdf, g.Competitors[i], xLeft, y, imgSize, colWidth)
			i++
		}
		if j < len(g.Staff) {
			drawPerson(pdf, g.Staff[j], xRight, y, imgSize, colWidth)
			j++
		}

		if i >= len(g.Competitors) && j >= len(g.Staff) {
			break
		}
		rowCount++
	}

	err := pdf.OutputFileAndClose("profiles.pdf")
	if err != nil {
		log.Fatalf("Could not draw hand-in pdf: %v", err)
	}
	fmt.Printf("Hand-in opened for %s\n", g.EventName)
}

// TODO Change this (DRY)
func (g *Group) DrawRoundPDF() {
	// TODO: Copy placeholder PDF
	if len(g.Competitors) == 0 {
		fmt.Println("Draw hand-in PDF: No competitors found.")
	}
	pdf := gofpdf.NewCustom(&gofpdf.InitType{
		UnitStr: "pt",
		Size: gofpdf.SizeType{
			Wd: 1920,
			Ht: 1080,
		},
	})

	pageMargin := 50.0
	sectionTitleHeight := 40.0

	colWidth := (1920 - pageMargin*3) / 2 // Two columns with spacing
	xLeft := pageMargin
	xRight := xLeft + colWidth + pageMargin

	competitorCount := len(g.Competitors) * 2 // Multiply by two for some reason
	if len(g.Staff) > competitorCount/2 {
		competitorCount = len(g.Staff) * 2
	}
	yStart := pageMargin + sectionTitleHeight + 20
	rows := math.Ceil(float64(competitorCount) / 2.0)

	infoLine := 50.0
	pageHeight := 1080.0
	availableHeight := pageHeight - yStart - pageMargin - infoLine

	lineHeight := availableHeight / rows
	imgSize := lineHeight - 5.0

	// Start first page
	pdf.AddPage()

	// Set page color
	pdf.SetTextColor(236, 240, 241)
	pdf.SetFillColor(44, 62, 80)
	pdf.Rect(0, 0, 1920, 1080, "F")

	// regular := filepath.Join(config.FontDir, "HackNerdFont-Regular.ttf")
	// bold := filepath.Join(config.FontDir, "HackNerdFont-Bold.ttf")
	pdf.AddUTF8Font("hack", "", "HackNerdFont-Regular.ttf")
	pdf.AddUTF8Font("hack", "B", "HackNerdFont-Bold.ttf")
	//    pdf.AddUTF8Font("hack", "", regular)
	// pdf.AddUTF8Font("hack", "B", bold)
	font := "hack"
	pdf.SetFont(font, "", 24)
	pdf.SetXY(10, 10)

	// Draw infoline
	infoHeight := pageHeight - 50
	pdf.SetLineWidth(60.0)
	pdf.SetDrawColor(83, 96, 242)
	pdf.Line(0, infoHeight, 1920, infoHeight)

	pdf.SetFont(font, "B", 36)
	pdf.SetXY(infoHeight, 10)
	info := fmt.Sprintf("Current round: %s", g.EventName)
	pdf.Text(60, infoHeight+12, info)

	// Define title of staff column based of whether we have assigned staff or not
	var staffTitle string

	if len(g.Staff) > 0 {
		staffTitle = "Staff"
	} else {
		staffTitle = "No staff assigned"
	}

	// Draw section titles
	pdf.SetFont(font, "B", 36)
	pdf.SetXY(xLeft, pageMargin-20)
	pdf.CellFormat(colWidth, sectionTitleHeight, "Competitors", "", 0, "L", false, 0, "")

	pdf.SetXY(xRight, pageMargin-20)
	pdf.CellFormat(colWidth, sectionTitleHeight, staffTitle, "", 0, "L", false, 0, "")

	pdf.SetFont(font, "", 24)

	maxHeight := 1080.0
	rowCount := 0
	i, j := 0, 0

	for {
		// Check if we're out of space
		y := yStart + float64(rowCount)*lineHeight
		if y+lineHeight > maxHeight-pageMargin {
			pdf.AddPage()
			// Redraw section titles
			pdf.SetFont(font, "B", 36)
			pdf.SetXY(xLeft, pageMargin)
			pdf.CellFormat(colWidth, sectionTitleHeight, "Competitors", "", 0, "L", false, 0, "")
			pdf.SetXY(xRight, pageMargin)
			pdf.CellFormat(colWidth, sectionTitleHeight, "Staff", "", 0, "L", false, 0, "")
			pdf.SetFont(font, "", 24)
			rowCount = 0
			y = yStart
		}

		// competitors := g.Competitors
		// sort.Slice(competitors, func(i, j int) bool {
		// 	return competitors[i].Name > competitors[j].Name
		// })
		// staff := g.Competitors
		// sort.Slice(staff, func(i, j int) bool {
		// 	return staff[i].Name > staff[j].Name
		// })
		if i < len(g.Competitors) {
			drawPerson(pdf, g.Competitors[i], xLeft, y, imgSize, colWidth)
			i++
		}
		if j < len(g.Staff) {
			drawPerson(pdf, g.Staff[j], xRight, y, imgSize, colWidth)
			j++
		}

		if i >= len(g.Competitors) && j >= len(g.Staff) {
			break
		}
		rowCount++
	}

	err := pdf.OutputFileAndClose("profiles.pdf")
	if err != nil {
		log.Fatalf("Could not output and close profiles.pdf: %v", err)
	}
}

func removeJPGFiles(dirPath string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".jpg") {
			fullPath := filepath.Join(dirPath, name)
			err := os.Remove(fullPath)
			if err != nil {
				fmt.Printf("Failed to delete %s: %v\n", fullPath, err)
			}
		}
	}

	return nil
}

func ConvertFilesToJPGs(dirPath string) error {
	removeJPGFiles(dirPath)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		inputPath := filepath.Join(dirPath, fileName)
		outputPath := filepath.Join(dirPath, fileName+".jpg")

		cmd := exec.Command("ffmpeg", "-loglevel", "quiet", "-i", inputPath, "-q:v", "2", outputPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		cmd.Run()
	}

	return nil
}

func drawPerson(pdf *gofpdf.Fpdf, person Person, x, y, imgSize, colWidth float64) {
	imgPath := filepath.Clean(fmt.Sprintf("%s.jpg", person.ImagePath()))
	fallbackPath := fmt.Sprintf("%s/placeholder.jpg", config.AppDataDir)

	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		imgPath = fallbackPath
	}

	if person.WcaId == "2021ELIA01" {
		imgPath = fallbackPath
	}

	// Draw image
	pdf.ImageOptions(imgPath, x, y, imgSize, imgSize, false, gofpdf.ImageOptions{ReadDpi: true}, 0, "")

	// Draw name to the right of the image
	textX := x + imgSize + 10
	textY := y + imgSize/2 - 8
	pdf.SetXY(textX, textY)
	pdf.CellFormat(colWidth-imgSize-10, 16, person.Name, "", 0, "L", false, 0, "")
}

type wcifSchedule struct {
	Venues []wcifVenue `json:"venues"`
}

type wcifVenue struct {
	ID    int        `json:"id"`
	Rooms []wcifRoom `json:"rooms"`
}
type wcifRoom struct {
	ID         int            `json:"id"`
	Activities []wcifActivity `json:"activities"`
}

type wcifActivity struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	ActivityCode string    `json:"activityCode"`
	Groups       []Group   `json:"childActivities"`
	Results      []Result  `json:"results"`
	StartTime    time.Time `json:"startTime"`
	EndTime      time.Time `json:"endTime"`
}

type wcifEvent struct {
	ID     string
	Rounds []wcifRound
}

type wcifRound struct {
	ID      string
	Results []Result
}

func (c *Competition) ReloadPersons() error {
	var raw struct {
		Persons []Person `json:"persons"`
	}

	data, err := FetchWCIF(c.ID)
	if err != nil {
		log.Fatal(err)
	}

	err = json.Unmarshal(data, &raw)
	if err != nil {
		return err
	}
	c.Persons = raw.Persons

	return nil
}

func BuildCompetitionFromWCIF(data []byte) (*Competition, error) {
	var raw struct {
		ID       string        `json:"id"`
		Name     string        `json:"name"`
		Schedule *wcifSchedule `json:"schedule"`
		// Groups   []Group  `json:"childActivities"`
		Persons []Person `json:"persons"`
	}

	err := json.Unmarshal(data, &raw)
	if err != nil {
		return nil, err
	}

	comp := &Competition{
		ID:      raw.ID,
		Name:    strings.ReplaceAll(raw.Name, "å", "a"),
		Rounds:  []Round{},
		Persons: []Person{},
	}

	if raw.Persons != nil {
		for _, p := range raw.Persons {
			comp.Persons = append(comp.Persons, p)
		}
	}

	if raw.Schedule != nil {
		for _, venue := range raw.Schedule.Venues {
			if venue.ID == 1 {
				for _, room := range venue.Rooms {
					if room.ID == 1 {
						for _, act := range room.Activities {
							if strings.Contains(act.ActivityCode, "other") {
								continue
							}
							comp.Rounds = append(comp.Rounds, Round{
								ID:           act.ID,
								EventName:    act.Name,
								EventId:      strings.Split(act.ActivityCode, "-")[0],
								Finished:     false,
								ActivityCode: act.ActivityCode,
								Groups:       act.Groups,
								StartTime:    act.StartTime,
								EndTime:      act.EndTime,
							})
						}
					}
				}
			}
		}
	}

	comp.LoadRoundData()
	comp.SortRounds()
	return comp, nil
}

// LoadCompetitionFromFile loads a Competition from a JSON file.
func LoadCompetitionFromFile(filename string) (*Competition, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var comp Competition
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&comp)
	if err != nil {
		return nil, err
	}

	return &comp, nil
}

func FetchWCIF(competitionID string) ([]byte, error) {
	url := fmt.Sprintf("https://www.worldcubeassociation.org/api/v0/competitions/%s/wcif/public", competitionID)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch WCIF: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}
