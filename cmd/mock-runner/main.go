// mock-runner prints a fixed demonstration; it accepts no commands or secrets.
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/grdamico/runawaybao/jobrunner"
)

func main() {
	runner := jobrunner.NewRunner(jobrunner.NewPolicy(jobrunner.PermissionExecute))
	jobs := []jobrunner.Job{
		{ID: "demo-success", Action: "succeed"},
		{ID: "demo-failure", Action: "fail"},
		{ID: "demo-unknown", Action: "unknown"},
	}
	encoder := json.NewEncoder(os.Stdout)
	deniedRunner := jobrunner.NewRunner(jobrunner.NewPolicy())
	if err := encoder.Encode(deniedRunner.Run(jobrunner.Job{ID: "demo-denied", Action: "succeed"})); err != nil {
		log.Fatal(err)
	}
	for _, job := range jobs {
		if err := encoder.Encode(runner.Run(job)); err != nil {
			log.Fatal(err)
		}
	}
}
