// generate-site-data reads the CCattler codebase and produces a JSON file
// with project statistics, milestone status, example configs, and DSL
// reference. The website imports this file to display live project data.
//
// Usage: go run scripts/generate-site-data.go
// Output: website/src/site-data.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type siteData struct {
	TestCount  int              `json:"testCount"`
	PackageCount int            `json:"packageCount"`
	Milestones []milestone      `json:"milestones"`
	Examples   []example        `json:"examples"`
	DSLFeatures []dslFeature    `json:"dslFeatures"`
	DemoCommands []demoCommand  `json:"demoCommands"`
}

type milestone struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Demo     string `json:"demo"`
	Complete bool   `json:"complete"`
}

type example struct {
	Name        string `json:"name"`
	Filename    string `json:"filename"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

type dslFeature struct {
	Keyword     string `json:"keyword"`
	Context     string `json:"context"`
	Description string `json:"description"`
}

type demoCommand struct {
	Command     string `json:"command"`
	Milestone   string `json:"milestone"`
	Description string `json:"description"`
}

func main() {
	data := siteData{}

	data.TestCount, data.PackageCount = countTests()
	data.Milestones = parseMilestones()
	data.Examples = readExamples()
	data.DSLFeatures = extractDSLFeatures()
	data.DemoCommands = listDemoCommands()

	outputBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json marshal: %v\n", err)
		os.Exit(1)
	}

	outputPath := filepath.Join("website", "src", "site-data.json")
	if err := os.WriteFile(outputPath, outputBytes, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", outputPath, err)
		os.Exit(1)
	}

	fmt.Printf("Generated %s: %d tests, %d packages, %d milestones, %d examples, %d DSL features\n",
		outputPath, data.TestCount, data.PackageCount,
		len(data.Milestones), len(data.Examples), len(data.DSLFeatures))
}

func countTests() (int, int) {
	cmd := exec.Command("go", "test", "./...", "-v", "-count=1")
	output, _ := cmd.CombinedOutput()

	testCount := 0
	packageCount := 0
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "--- PASS:") {
			testCount++
		}
		if strings.HasPrefix(line, "ok ") {
			packageCount++
		}
	}
	return testCount, packageCount
}

func parseMilestones() []milestone {
	milestones := []milestone{
		{ID: "M1", Name: "State Machine", Phase: "Phases 0-4", Demo: "Apply config, see facts reconcile"},
		{ID: "M2", Name: "Single Machine", Phase: "Phase 5", Demo: "CLI -> parser -> store -> reconciler -> running"},
		{ID: "M3", Name: "Distributed", Phase: "Phase 6", Demo: "10 instances spread across 3 nodes"},
		{ID: "M4", Name: "Networking", Phase: "Phase 7", Demo: "Services reachable by name, traffic balances"},
		{ID: "M5", Name: "Storage", Phase: "Phase 8", Demo: "Persistent volumes survive node moves"},
		{ID: "M6", Name: "Resilient", Phase: "Phase 9", Demo: "Kill anything, cluster converges"},
		{ID: "M7", Name: "Smart", Phase: "Phase 10", Demo: "Autoscaling, rolling deploys, placement policies"},
		{ID: "M8", Name: "Secure", Phase: "Phase 11", Demo: "mTLS, RBAC+ABAC, secrets, audit"},
		{ID: "M9", Name: "Multi-tenant", Phase: "Phase 12", Demo: "Tenant isolation, quotas, fair scheduling"},
		{ID: "M10", Name: "Production", Phase: "Phase 13", Demo: "Observability, HA control plane, extensibility"},
	}

	claudeContent, err := os.ReadFile("CLAUDE.md")
	if err != nil {
		return milestones
	}
	content := string(claudeContent)

	// Match individual "Milestone M5 — Storage: COMPLETE" on a single line.
	for i := range milestones {
		individualPattern := fmt.Sprintf(`(?im)^.*Milestone\s+%s\s.*COMPLETE`, milestones[i].ID)
		if matched, _ := regexp.MatchString(individualPattern, content); matched {
			milestones[i].Complete = true
		}
	}
	// Parse range patterns like "M1–M5 complete" or "M1-M5 complete".
	rangePattern := regexp.MustCompile(`(?i)M(\d+)[–-]M(\d+)\s+complete`)
	rangeMatches := rangePattern.FindAllStringSubmatch(content, -1)
	for _, rangeMatch := range rangeMatches {
		var rangeStart, rangeEnd int
		fmt.Sscanf(rangeMatch[1], "%d", &rangeStart)
		fmt.Sscanf(rangeMatch[2], "%d", &rangeEnd)
		for i := range milestones {
			var milestoneNumber int
			fmt.Sscanf(strings.TrimPrefix(milestones[i].ID, "M"), "%d", &milestoneNumber)
			if milestoneNumber >= rangeStart && milestoneNumber <= rangeEnd {
				milestones[i].Complete = true
			}
		}
	}

	return milestones
}

func readExamples() []example {
	exampleFiles, err := filepath.Glob("examples/*.ccattler")
	if err != nil {
		return nil
	}
	sort.Strings(exampleFiles)

	var examples []example
	for _, filePath := range exampleFiles {
		content, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		filename := filepath.Base(filePath)
		description := extractFirstComment(string(content))

		examples = append(examples, example{
			Name:        strings.TrimSuffix(filename, ".ccattler"),
			Filename:    filename,
			Description: description,
			Content:     string(content),
		})
	}
	return examples
}

func extractFirstComment(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
		return strings.TrimPrefix(strings.TrimSpace(lines[0]), "# ")
	}
	return ""
}

func extractDSLFeatures() []dslFeature {
	return []dslFeature{
		{Keyword: "service", Context: "top-level", Description: "Declares a named service with container image, instances, ports, resources, health checks, and volume mounts"},
		{Keyword: "image", Context: "service", Description: "Container image reference (e.g. nginx:1.28) or process command for process runtime"},
		{Keyword: "instances", Context: "service", Description: "Desired number of running instances for the service"},
		{Keyword: "expose", Context: "service", Description: "Port number to expose from each instance"},
		{Keyword: "resources", Context: "service", Description: "Block declaring CPU and memory resource constraints (e.g. cpu 500m, memory 512Mi)"},
		{Keyword: "health", Context: "service", Description: "Block declaring health check configuration: http with path, tcp, and interval"},
		{Keyword: "volume", Context: "top-level", Description: "Declares a named persistent volume with size and persistence flag"},
		{Keyword: "volume", Context: "service", Description: "Mounts a named volume at a filesystem path inside the service's instances"},
		{Keyword: "size", Context: "volume", Description: "Storage capacity in Kubernetes-style units (e.g. 100Gi)"},
		{Keyword: "persistent", Context: "volume", Description: "Boolean flag: true means volume data survives instance deletion"},
	}
}

func listDemoCommands() []demoCommand {
	return []demoCommand{
		{Command: "cca demo", Milestone: "M2", Description: "Single node, 3 instances, simulated runtime"},
		{Command: "cca demo-distributed", Milestone: "M3", Description: "3 nodes, kills one, shows rescheduling"},
		{Command: "cca demo-network", Milestone: "M4", Description: "VIPs, DNS, round-robin load balancing"},
		{Command: "cca demo-storage", Milestone: "M5", Description: "Persistent volume migrates when node dies"},
	}
}
