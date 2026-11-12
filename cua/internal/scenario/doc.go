// Package scenario is fennec-cua's live scenario suite (T2 fixture
// scenarios, T3 comparisons with shuttle, T4 real apps). Its tests build
// only with the scenario tag and run inside the test VM, never on a
// developer's desktop:
//
//	go run ./cmd/cua-vm test -tags scenario ./internal/scenario -- -test.timeout 3h
//
// Each test resets CUAFixture (or prepares a real app), drives fennec-cua
// and, for oracle capabilities, shuttle over MCP, one backend at a time,
// and judges the outcome from the fixture's true state or from
// accessibility reads. The results land in a JSON report
// (-scenario.report, default /Users/admin/cua/scenario-report.json).
package scenario
