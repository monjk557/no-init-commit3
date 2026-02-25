package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
)

// DemoRunner represents a runnable demo
type DemoRunner struct {
	Name        string
	Description string
	RunFunc     func()
}

// Available demos registry
var demos = map[string]DemoRunner{
	"production-sim": {
		Name:        "production-sim",
		Description: "Production scenario simulation for testing merge protection",
		RunFunc:     runProductionSimulation,
	},
	"protection-test": {
		Name:        "protection-test",
		Description: "Real-world protection test scenarios",
		RunFunc:     runProtectionTest,
	},
	"pagination-test": {
		Name:        "pagination-test",
		Description: "Smart pagination performance testing",
		RunFunc:     runPaginationTest,
	},
	"cicd-workflow": {
		Name:        "cicd-workflow",
		Description: "CI/CD workflow simulation testing",
		RunFunc:     runCICDWorkflow,
	},
	"test-runner": {
		Name:        "test-runner",
		Description: "Comprehensive production test runner",
		RunFunc:     runTestRunner,
	},
	"scenario-sim": {
		Name:        "scenario-sim",
		Description: "Production scenario simulator",
		RunFunc:     runScenarioSimulator,
	},
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "CollabOps Git Executor - Demo Suite\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [demo-name]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Available demos:\n")
		listAvailableDemos()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s production-sim    # Run production simulation\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s protection-test   # Run protection tests\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s list              # List all demos\n", os.Args[0])
	}

	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Println("🎯 CollabOps Git Executor - Demo Suite")
		fmt.Println()
		listAvailableDemos()
		fmt.Println()
		fmt.Println("Use --help for usage information")
		return
	}

	demoName := flag.Arg(0)

	if demoName == "list" {
		fmt.Println("📋 Available Demos:")
		listAvailableDemos()
		return
	}

	demo, exists := demos[demoName]
	if !exists {
		fmt.Printf("❌ Error: Demo '%s' not found\n\n", demoName)
		listAvailableDemos()
		os.Exit(1)
	}

	fmt.Printf("🚀 Running Demo: %s\n", demo.Description)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	demo.RunFunc()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("✅ Demo '%s' completed\n", demoName)
}

func listAvailableDemos() {
	var names []string
	for name := range demos {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		demo := demos[name]
		fmt.Printf("  %-18s %s\n", name, demo.Description)
	}
}

// Demo implementations (placeholder functions)
// These will be implemented by copying logic from individual demo files

func runProductionSimulation() {
	fmt.Println("🏭 Production Simulation Demo")
	fmt.Println("This demo simulates production merge scenarios with environment file protection")
	fmt.Println("TODO: Implement production simulation logic from demo_production_simulation")
}

func runProtectionTest() {
	fmt.Println("🛡️ Protection Test Demo")
	fmt.Println("This demo tests real-world file protection scenarios")
	fmt.Println("TODO: Implement protection test logic from real_world_protection_test")
}

func runPaginationTest() {
	fmt.Println("📄 Pagination Test Demo")
	fmt.Println("This demo tests smart pagination performance")
	fmt.Println("TODO: Implement pagination test logic from test-pagination")
}

func runCICDWorkflow() {
	fmt.Println("🔄 CI/CD Workflow Demo")
	fmt.Println("This demo simulates CI/CD workflow scenarios")
	fmt.Println("TODO: Implement CI/CD workflow logic from ci_cd_workflow_simulator")
}

func runTestRunner() {
	fmt.Println("🧪 Test Runner Demo")
	fmt.Println("This demo runs comprehensive production tests")
	fmt.Println("TODO: Implement test runner logic from comprehensive_production_test_runner")
}

func runScenarioSimulator() {
	fmt.Println("🎭 Scenario Simulator Demo")
	fmt.Println("This demo simulates various production scenarios")
	fmt.Println("TODO: Implement scenario simulator logic from production_scenario_simulator")
}
