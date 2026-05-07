package autoload

import "testing"

func TestExcludePatternBasic(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"tests/", "tests/Foo.php", true},
		{"tests/", "tests/sub/Foo.php", true},
		{"tests/", "src/tests/Foo.php", false},
		{"**/Tests/", "src/Tests/A.php", true},
		{"**/Tests/", "deep/sub/Tests/A.php", true},
		{"**/Tests/", "src/A.php", false},
		{"**/*Test.php", "src/FooTest.php", true},
		{"**/*Test.php", "src/Foo.php", false},
		{"src/legacy/", "src/legacy/old.php", true},
	}
	for _, tc := range cases {
		m, err := compileExclude([]string{tc.pat})
		if err != nil {
			t.Errorf("%s: compile: %v", tc.pat, err)
			continue
		}
		if got := m.Match(tc.path); got != tc.want {
			t.Errorf("pattern=%s path=%s got=%v want=%v", tc.pat, tc.path, got, tc.want)
		}
	}
}

func TestExcludeMultiplePatterns(t *testing.T) {
	m, _ := compileExclude([]string{"**/Tests/", "**/Fixtures/"})
	if !m.Match("src/Foo/Tests/X.php") {
		t.Errorf("Tests/ should match")
	}
	if !m.Match("src/Foo/Fixtures/X.php") {
		t.Errorf("Fixtures/ should match")
	}
	if m.Match("src/Foo/X.php") {
		t.Errorf("plain src path should not match")
	}
}

func TestExcludeEmptyMatchesNothing(t *testing.T) {
	m, _ := compileExclude(nil)
	if m.Match("anything") {
		t.Errorf("empty matcher must match nothing")
	}
}
