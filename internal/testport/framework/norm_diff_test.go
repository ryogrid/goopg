package framework

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"strings"
)

func TestNormDiffCase(t *testing.T) {
	expectedBytes, _ := os.ReadFile("../../../postgres/src/test/regress/expected/case.out")
	actualBytes, _ := os.ReadFile("/tmp/case_test_actual2.txt")
	
	normExpected := NormalizeRegressOutput(string(expectedBytes))
	normActual := NormalizeRegressOutput(string(actualBytes))
	
	os.WriteFile("/tmp/case_norm_expected.txt", []byte(normExpected), 0644)
	os.WriteFile("/tmp/case_norm_actual.txt", []byte(normActual), 0644)
	
	out, _ := exec.Command("diff", "/tmp/case_norm_expected.txt", "/tmp/case_norm_actual.txt").Output()
	fmt.Println(string(out))
	fmt.Printf("\nDiff lines: %d\n", strings.Count(string(out), "\n"))
}
