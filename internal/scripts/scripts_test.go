package scripts

import "testing"

func TestClassifyShell(t *testing.T) {
	cases := []string{
		"echo hello",
		"php artisan key:generate",
		"npm run build",
		"@php artisan migrate", // leading @ but with whitespace -> shell w/ alias prefix is NOT supported here; it's an @ref only when the entire body is "@name". This is a shell command starting with @php.
	}
	for _, c := range cases {
		k, _, _, err := classify(c)
		if err != nil {
			t.Errorf("classify(%q) err = %v", c, err)
			continue
		}
		if k != formShell {
			t.Errorf("classify(%q) = %v, want shell", c, k)
		}
	}
}

func TestClassifyPHPCallable(t *testing.T) {
	cases := map[string]struct{ class, method string }{
		`App\Bootstrap::run`:            {"App\\Bootstrap", "run"},
		`\Vendor\Pkg\Hooks::postInstall`: {"\\Vendor\\Pkg\\Hooks", "postInstall"},
		`Class::m`:                      {"Class", "m"},
	}
	for body, want := range cases {
		k, class, method, err := classify(body)
		if err != nil {
			t.Errorf("classify(%q) err = %v", body, err)
			continue
		}
		if k != formPHPCallable {
			t.Errorf("classify(%q) form = %v, want phpCallable", body, k)
		}
		if class != want.class || method != want.method {
			t.Errorf("classify(%q) = (%q,%q), want (%q,%q)", body, class, method, want.class, want.method)
		}
	}
}

func TestClassifyRef(t *testing.T) {
	k, name, _, err := classify("@build-assets")
	if err != nil {
		t.Fatal(err)
	}
	if k != formRef || name != "build-assets" {
		t.Errorf("classify @build-assets = (%v, %q)", k, name)
	}
}

func TestRedactBody(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	got := redactBody(long)
	if len(got) > 103 {
		t.Errorf("redacted len = %d, want <=103", len(got))
	}
}
