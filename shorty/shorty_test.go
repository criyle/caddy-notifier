package shorty

import (
	"testing"
)

const testString = "First test string, hello world! Hello world? Hello world!"

func test(t *testing.T, original string, tokenSize int) {
	deflated := NewShorty(tokenSize).deflate([]byte(original))
	inflated := string(NewShorty(tokenSize).inflate(deflated))
	t.Log()
	t.Log("original", len(original), original)
	t.Log("deflated", len(deflated), deflated)
	t.Log("inflated", len(inflated), inflated)
	if inflated != original {
		t.FailNow()
	}
}

func test2(t *testing.T, original string, original2 string, tokenSize int) {
	d := NewShorty(tokenSize)
	i := NewShorty(tokenSize)

	t.Log("original", len(original), original)
	t.Log("original", len(original2), original2)

	deflated := d.deflate([]byte(original))
	t.Log("deflated", len(deflated), deflated)

	inflated := string(i.inflate(deflated))
	t.Log("inflated", len(inflated), inflated)

	deflated2 := d.deflate([]byte(original2))
	t.Log("deflated", len(deflated2), deflated2)

	inflated2 := string(i.inflate(deflated2))
	t.Log("inflated", len(inflated2), inflated2)

	if inflated != original {
		t.FailNow()
	}
	if inflated2 != original2 {
		t.FailNow()
	}
}

func BenchmarkDeflate(b *testing.B) {
	d := NewShorty(10)
	for b.Loop() {
		d.deflate([]byte(testString))
	}
}

func BenchmarkInflate(b *testing.B) {
	d := NewShorty(10)
	i := NewShorty(10)
	def := d.deflate([]byte(testString))
	for b.Loop() {
		i.Reset(true)
		i.inflate(def)
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
