package detect

import "testing"

func TestSourceRecognizesEachLanguage(t *testing.T) {
	cases := []struct {
		name string
		want Language
		code string
	}{
		{"java class", Java, `
public class Hello {
    public static void main(String[] args) {
        System.out.println("hi");
    }
}`},
		{"kotlin suspend", Kotlin, `
import kotlinx.coroutines.delay

suspend fun greet(name: String): String {
    delay(10)
    val message = "hi $name"
    return message
}`},
		{"kotlin data class", Kotlin, `
data class Point(val x: Int, val y: Int) {
    companion object { val origin = Point(0, 0) }
}`},
		{"groovy script", Groovy, `
def numbers = [1, 2, 3]
numbers.each { println "n=$it" }`},
		{"scala case class", Scala, `
case class Point(x: Int, y: Int)

object Main {
  def main(args: Array[String]): Unit = println(Point(1, 2))
}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			guess := Source(c.code)
			if guess.Language != c.want {
				t.Errorf("Source() = %q, want %q", guess.Language, c.want)
			}
		})
	}
}

func TestSourceGivesUpOnSomethingElse(t *testing.T) {
	for _, code := range []string{"", "hello there", "SELECT * FROM users WHERE id = 1"} {
		if guess := Source(code); guess.Language != Unknown || guess.Confident {
			t.Errorf("Source(%q) = %+v, want an unconfident Unknown", code, guess)
		}
	}
}

// A Java file with Kotlin in a comment is still a Java file.
func TestCommentsDoNotVote(t *testing.T) {
	code := `
// suspend fun old() = 1
/* data class Legacy(val x: Int)
   companion object { } */
public class Hello {
    public static void main(String[] args) {
        System.out.println("hi");
    }
}`
	if guess := Source(code); guess.Language != Java {
		t.Errorf("Source() = %q, want java", guess.Language)
	}
}

func TestByExtensionBeatsGuessing(t *testing.T) {
	cases := map[string]Language{
		"Main.java":     Java,
		"main.kt":       Kotlin,
		"build.gradle":  Groovy,
		"Script.KTS":    Kotlin,
		"App.scala":     Scala,
		"notes.txt":     Unknown,
		"Makefile":      Unknown,
		"a/b/c/Main.kt": Kotlin,
	}
	for filename, want := range cases {
		if got := ByExtension(filename); got != want {
			t.Errorf("ByExtension(%q) = %q, want %q", filename, got, want)
		}
	}
}

func TestUnterminatedBlockCommentDoesNotHang(t *testing.T) {
	if guess := Source("/* never closed\nfun x() = 1"); guess.Language != Unknown {
		t.Errorf("Source() = %q, want unknown", guess.Language)
	}
}
