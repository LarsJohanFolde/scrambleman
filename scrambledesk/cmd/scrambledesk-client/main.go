package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/larsjohanfolde/scrambleman/scrambledesk/config"
	"github.com/larsjohanfolde/scrambleman/scrambledesk/internal/models"
)

func main() {
	next := flag.Bool("n", false, "Open the next scramble file")
	close := flag.Bool("c", false, "Close the active scramble set")
	persons := flag.Bool("reload-competitors", false, "Reload the registered competitors")
	openScrambleSet := flag.String("o", "", "Open a spesific scramble set")
	startFrom := flag.String("start-from", "", "Mark all previous rounds as finished and start from the inputted round")
	ip := flag.String("ip", "", "Define the server IP and store this for future use")
	competitionId := flag.String("init", "", "Load a competition ID")
	export := flag.Bool("export", false, "Export the competition data to a json file")
	debug := flag.Bool("debug", false, "Debug")
	flag.Parse()

	if *persons {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}

		comp.Save()
	}

	if *ip != "" {
		err := os.WriteFile(config.IpFile, []byte(*ip), 0644)
		if err != nil {
			log.Fatalf("Could not save IP to file: %v", err)
		}
	}

	if *debug {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}
		comp.SortRounds()

		for _, r := range comp.Rounds {
			fmt.Printf("%s: RoundNumber (%d), GroupCount (%d) [Finished = %t]\n", r.EventName, r.RoundNumber, r.GroupCount, r.Finished)
			for _, g := range r.Groups {
				fmt.Printf("\t%s: GroupNumber (%d) [Finished = %t]\n", g.EventName, g.GroupNumber, g.Finished)
			}
		}
	}

	if *openScrambleSet != "" {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}

		err = comp.OpenScrambleSet(*openScrambleSet)
		if err != nil {
			log.Fatalf("Could not open scramble set: %v\n", err)
		}

		comp.Save()
	}

	if *startFrom != "" {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}
		comp.StartFrom(*startFrom)

		comp.Save()
	}

	if *competitionId != "" {
		data, err := models.FetchWCIF(*competitionId)
		if err != nil {
			log.Fatal(err)
		}

		comp, err := models.BuildCompetitionFromWCIF(data)
		if err != nil {
			log.Fatal(err)
		}

		err = comp.LoadAvatars()
		if err != nil {
			fmt.Printf("Could not load avatar: %v\n", err)
		}

		err = comp.Save()
		if err != nil {
			log.Fatal(err)
		}
	}

	if *close {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}
		group := comp.NextGroup()
		// Ensure we have the competitors for the advanced rounds
		if group.RoundNumber > 1 {
			comp.AssignAdvancedRoundCompetitors()
		}
		group.DrawHandInPDF()

		f := fmt.Sprintf("file=@%s/templates/intermission.pdf", config.AppDataDir)
		cmd := exec.Command(
			"curl",
			"--cert", config.ClientCrt,
			"--key", config.ClientKey,
			"--cacert", config.CaCrt,
			"-F", f,
			config.ScrambleURL,
		)

		_, err = cmd.CombinedOutput()
		if err != nil {
			log.Fatal(err)
		}

		group.ClosedTimestamp = append(group.ClosedTimestamp, time.Now())

		cmd = exec.Command(
			"curl",
			"--cert", config.ClientCrt,
			"--key", config.ClientKey,
			"--cacert", config.CaCrt,
			"-F", "file=@profiles.pdf",
			config.CompetitorListURL,
		)

		_, err = cmd.CombinedOutput()
		if err != nil {
			log.Fatal(err)
		}
	}

	if *next {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}
		comp.SortRounds()

		comp.StartNextGroup()
		comp.Save()
	}

	if *export {
		comp, err := loadCompetition()
		if err != nil {
			log.Fatal(err)
		}

		err = comp.Export()
		if err != nil {
			log.Fatalf("Could not export competition: %v\n", err)
		}
	}
}

func loadCompetition() (*models.Competition, error) {
	saveLocation := fmt.Sprintf("%s/competition.json", config.AppDataDir)
	comp, err := models.LoadCompetitionFromFile(saveLocation)
	if err != nil {
		return nil, err
	}

	return comp, nil
}
