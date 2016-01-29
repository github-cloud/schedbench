package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/jobspec"
	"github.com/hashicorp/nomad/nomad/structs"
)

var numContainers, numJobs, totalContainers int

func main() {
	// Check the args
	if len(os.Args) != 2 {
		log.Fatalln("usage: nomad-docker <command>")
	}

	// Parse the env vars into globals
	var err error
	v := os.Getenv("NOMAD_NUM_CONTAINERS")
	if numContainers, err = strconv.Atoi(v); err != nil {
		log.Fatalln("NOMAD_NUM_CONTAINERS must be numeric")
	}
	v = os.Getenv("NOMAD_NUM_JOBS")
	if numJobs, err = strconv.Atoi(v); err != nil {
		log.Fatalln("NOMAD_NUM_JOBS must be numeric")
	}
	totalContainers = numContainers * numJobs

	// Switch on the command
	switch os.Args[1] {
	case "setup":
		os.Exit(handleSetup())
	case "run":
		os.Exit(handleRun())
	case "status":
		os.Exit(handleStatus())
	case "teardown":
		os.Exit(handleTeardown())
	default:
		log.Fatalf("unknown command: %q", os.Args[1])
	}
}

func handleSetup() int {
	// Create the job file
	fh, err := os.Create("job.nomad")
	if err != nil {
		log.Fatalf("failed creating job file: %v", err)
	}
	defer fh.Close()

	// Write the job contents
	jobContent := fmt.Sprintf(jobTemplate, numContainers)
	if _, err := fh.WriteString(jobContent); err != nil {
		log.Fatalf("failed writing to job file: %v", err)
	}
	return 0
}

func handleRun() int {
	// Parse the job file
	job, err := jobspec.ParseFile("job.nomad")
	if err != nil {
		log.Fatalf("failed parsing job file: %v", err)
	}

	// Convert to an API struct for submission
	apiJob, err := convertStructJob(job)
	if err != nil {
		log.Fatalf("failed converting job: %v", err)
	}

	// Get the API client
	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		log.Fatalf("failed creating nomad client: %v", err)
	}
	jobs := client.Jobs()

	// Submit the job the requested number of times
	for i := 0; i < numJobs; i++ {
		// Increment the job ID
		apiJob.ID = fmt.Sprintf("bench-docker-%d", i)
		if _, _, err := jobs.Register(apiJob, nil); err != nil {
			log.Fatalf("failed registering jobs: %v", err)
		}
	}

	return 0
}

func handleStatus() int {
	// Get the API client
	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		log.Fatalf("failed creating nomad client: %v", err)
	}
	allocs := client.Allocations()

	// Wait loop for allocation statuses
	var lastRunning, lastTotal int64
	var index uint64 = 1
	for {
		// Set up the args
		args := &api.QueryOptions{
			AllowStale: true,
			WaitIndex:  index,
		}

		// Start the query
		resp, qm, err := allocs.List(args)
		if err != nil {
			// Only log and continue to skip minor errors
			log.Printf("failed querying allocations: %v", err)
			continue
		}

		// Check the index
		if qm.LastIndex == index {
			continue
		}
		index = qm.LastIndex

		// Check the response
		var allocsTotal, allocsRunning int64
		for _, alloc := range resp {
			if alloc.DesiredStatus == structs.AllocDesiredStatusRun {
				allocsTotal++
			}
			if alloc.ClientStatus == structs.AllocClientStatusRunning {
				allocsRunning++
			}
		}

		// Write the metrics, if there were changes.
		if allocsTotal != lastTotal {
			lastTotal = allocsTotal
			fmt.Fprintf(os.Stdout, "placed|%f\n", float64(allocsTotal))
		}
		if allocsRunning != lastRunning {
			lastRunning = allocsRunning
			fmt.Fprintf(os.Stdout, "running|%f\n", float64(allocsRunning))
		}

		// Break out if all of our allocs are running
		if allocsRunning == int64(totalContainers) {
			break
		}
	}

	return 0
}

func handleTeardown() int {
	// Get the API client
	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		log.Fatalf("failed creating nomad client: %v", err)
	}

	// Iterate all of the jobs and stop them
	jobs, _, err := client.Jobs().List(nil)
	if err != nil {
		log.Fatalf("failed listing jobs: %v", err)
	}
	for _, job := range jobs {
		if _, _, err := client.Jobs().Deregister(job.ID, nil); err != nil {
			log.Fatalf("failed deregistering job: %v", err)
		}
	}
	return 0
}

func convertStructJob(in *structs.Job) (*api.Job, error) {
	gob.Register([]map[string]interface{}{})
	gob.Register([]interface{}{})
	var apiJob *api.Job
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(in); err != nil {
		return nil, err
	}
	if err := gob.NewDecoder(buf).Decode(&apiJob); err != nil {
		return nil, err
	}
	return apiJob, nil
}

const jobTemplate = `
job "bench-docker" {
	datacenters = ["dc1"]

	group "cache" {
		count = %d

		restart {
			mode = "fail"
			attempts = 0
		}

		task "bench" {
			driver = "docker"

			config {
				image = "redis:latest"
			}

			resources {
				cpu = 100
				memory = 100
			}
		}
	}
}
`
