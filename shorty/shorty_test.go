package shorty

import (
	"testing"
)

const testString = "First test string, hello world! Hello world? Hello world!"

func test(t *testing.T, original string, tokenSize int) {
	Deflated := NewShorty(tokenSize).Deflate([]byte(original))
	Inflated := string(NewShorty(tokenSize).Inflate(Deflated))
	t.Log()
	t.Log("original", len(original), original)
	t.Log("Deflated", len(Deflated), Deflated)
	t.Log("Inflated", len(Inflated), Inflated)
	if Inflated != original {
		t.FailNow()
	}
}

func test2(t *testing.T, original string, original2 string, tokenSize int) {
	d := NewShorty(tokenSize)
	i := NewShorty(tokenSize)

	t.Log("original", len(original), original)
	t.Log("original", len(original2), original2)

	Deflated := d.Deflate([]byte(original))
	t.Log("Deflated", len(Deflated), Deflated)

	Inflated := string(i.Inflate(Deflated))
	t.Log("Inflated", len(Inflated), Inflated)

	Deflated2 := d.Deflate([]byte(original2))
	t.Log("Deflated", len(Deflated2), Deflated2)

	Inflated2 := string(i.Inflate(Deflated2))
	t.Log("Inflated", len(Inflated2), Inflated2)

	if Inflated != original {
		t.FailNow()
	}
	if Inflated2 != original2 {
		t.FailNow()
	}
}

func BenchmarkDeflate(b *testing.B) {
	d := NewShorty(10)
	for b.Loop() {
		d.Deflate([]byte(testString))
	}
}

func BenchmarkInflate(b *testing.B) {
	d := NewShorty(10)
	i := NewShorty(10)
	def := d.Deflate([]byte(testString))
	for b.Loop() {
		i.Reset(true)
		i.Inflate(def)
	}
}

func TestInflatedDeflated(t *testing.T) {
	t.Run("first", func(t *testing.T) {
		test(t, testString, 10)
	})
	t.Run("utf8", func(t *testing.T) {
		test(t, "<span class=\"icon icon-refresh\"></span> �má ÍK 测试 123</span>", 10)
	})
	t.Run("utf82", func(t *testing.T) {
		test(t, "0AA1", 10)
	})
}

func FuzzInflatedDeflated1(f *testing.F) {
	f.Add("hello", 10)
	f.Fuzz(func(t *testing.T, a string, b int) {
		if b <= 1 || b > 15 {
			t.Skip()
		}
		test(t, a, 10)
	})
}

func FuzzInflatedDeflated2(f *testing.F) {
	f.Add("hello", "world")
	f.Fuzz(func(t *testing.T, a string, b string) {
		test2(t, a, b, 10)
	})
}
