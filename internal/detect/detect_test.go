package detect

import "testing"

// Regression set: each snippet must be detected correctly and confidently. It was written
// alongside the signals, so passing it says nothing about accuracy on real code; measure that on
// an external corpus.
func TestSource(t *testing.T) {
	cases := []struct {
		name string
		code string
		want Language
	}{
		{"Java/8 android", "package com.example.app;\n\nimport android.os.Bundle;\nimport java.util.List;\nimport java.util.ArrayList;\n\npublic class MainActivity extends AppCompatActivity implements View.OnClickListener {\n    private final List<String> items = new ArrayList<>();\n\n    @Override\n    protected void onCreate(Bundle savedInstanceState) {\n        super.onCreate(savedInstanceState);\n        items.stream().filter(s -> s.length() > 3).forEach(System.out::println);\n    }\n\n    @Override\n    public void onClick(View v) {\n        int count = items.size();\n        if (count > 0) { Log.d(\"TAG\", \"clicked \" + count); }\n    }\n}\n", Java},
		{"Java/17 record/sealed", "package com.example.domain;\n\npublic sealed interface Shape permits Circle, Square {}\n\npublic record Circle(double radius) implements Shape {}\npublic record Square(double side) implements Shape {}\n\nfinal class Area {\n    static double of(Shape s) {\n        if (s instanceof Circle c) {\n            return Math.PI * c.radius() * c.radius();\n        }\n        return 0;\n    }\n}\n", Java},
		{"Java/21 switch patterns", "static String describe(Object o) {\n    return switch (o) {\n        case Integer i when i > 10 -> \"big int\";\n        case Integer i -> \"int\";\n        case String s -> {\n            var len = s.length();\n            yield \"str \" + len;\n        }\n        default -> \"other\";\n    };\n}\n", Java},
		{"Java/25 compact source", "void main() {\n    var name = IO.readln(\"Name: \");\n    IO.println(\"Hello, \" + name);\n}\n", Java},
		{"Java/snippet stream", "List<Order> paid = orders.stream()\n    .filter(o -> o.getStatus() == Status.PAID)\n    .sorted(Comparator.comparing(Order::getCreatedAt))\n    .collect(Collectors.toList());\n", Java},
		{"Kotlin/android", "package com.example.app\n\nimport android.os.Bundle\nimport androidx.appcompat.app.AppCompatActivity\nimport kotlinx.coroutines.launch\n\nclass MainActivity : AppCompatActivity() {\n    private val viewModel: MainViewModel by viewModels()\n    private lateinit var adapter: ItemsAdapter\n\n    override fun onCreate(savedInstanceState: Bundle?) {\n        super.onCreate(savedInstanceState)\n        lifecycleScope.launch {\n            viewModel.items.collect { adapter.submit(it) }\n        }\n    }\n\n    private fun render(state: UiState) {\n        when (state) {\n            is UiState.Loading -> showProgress()\n            is UiState.Data -> adapter.submit(state.items)\n        }\n    }\n}\n", Kotlin},
		{"Kotlin/1.9 modern", "sealed interface Result<out T> {\n    data class Ok<T>(val value: T) : Result<T>\n    data object Empty : Result<Nothing>\n}\n\n@JvmInline\nvalue class UserId(val raw: String)\n\nfun interface Mapper { fun map(x: Int): Int }\n\nfun <T> List<T>.second(): T? = getOrNull(1)\n", Kotlin},
		{"Kotlin/test backticks", "class RepoTest {\n    private val repo = FakeRepo()\n\n    @Test\n    fun `returns empty list when no data`() = runTest {\n        val result = repo.load()\n        assertEquals(emptyList<Item>(), result)\n    }\n}\n", Kotlin},
		{"Kotlin/snippet no fun", "val users = repository.findAll()\n    .filter { it.isActive }\n    .map { it.name.uppercase() }\nval first = users.firstOrNull() ?: \"none\"\nprintln(first)\n", Kotlin},
		{"Kotlin/kmp expect", "expect class Platform() {\n    val name: String\n}\n\nexpect fun currentTimeMillis(): Long\n", Kotlin},
		{"Kotlin/gradle kts", "plugins {\n    id(\"com.android.application\")\n    kotlin(\"android\")\n}\n\ndependencies {\n    implementation(\"androidx.core:core-ktx:1.13.1\")\n    testImplementation(\"junit:junit:4.13.2\")\n}\n", Kotlin},
		{"Groovy/script", "def names = ['alice', 'bob', 'carol']\ndef ages = [alice: 30, bob: 25]\nnames.each { name ->\n    println \"Hello, ${name}\"\n}\ndef adults = ages.findAll { k, v -> v >= 18 }\nassert 'abc' ==~ /a.c/\n", Groovy},
		{"Groovy/java-style class", "import groovy.transform.CompileStatic\n\n@CompileStatic\nclass Greeter {\n    String greet(String name) {\n        return \"Hello, $name\"\n    }\n\n    static void main(String[] args) {\n        println new Greeter().greet('world')\n    }\n}\n", Groovy},
		{"Groovy/spock", "class CalculatorSpec extends Specification {\n    def \"adds two numbers\"() {\n        given:\n        def calc = new Calculator()\n\n        expect:\n        calc.add(a, b) == c\n\n        where:\n        a | b || c\n        1 | 2 || 3\n    }\n}\n", Groovy},
		{"Groovy/gradle", "plugins {\n    id 'com.android.application'\n}\n\napply plugin: 'kotlin-android'\n\ndependencies {\n    implementation 'androidx.core:core-ktx:1.13.1'\n    testImplementation 'junit:junit:4.13.2'\n}\n", Groovy},
		{"Groovy/jenkinsfile", "pipeline {\n    agent any\n    stages {\n        stage('Build') {\n            steps {\n                sh './gradlew assemble'\n            }\n        }\n    }\n}\n", Groovy},
		{"Groovy/groovy3 ops", "def cfg = loadConfig()\ncfg.timeout ?= 30\nif (value !instanceof String) {\n    def names = people*.name\n    def sorted = names.sort { a, b -> a <=> b }\n}\n", Groovy},
		{"Scala/2 classic", "package com.example\n\nimport scala.concurrent.Future\nimport akka.actor._\n\ncase class User(id: Long, name: String)\n\nobject UserService extends App {\n  implicit val ec: ExecutionContext = ExecutionContext.global\n\n  def find(id: Long): Future[Option[User]] = Future {\n    users.get(id)\n  }\n\n  users.values.foreach { u =>\n    u match {\n      case User(_, name) if name.nonEmpty => println(name)\n      case _ => ()\n    }\n  }\n}\n", Scala},
		{"Scala/3 braceless", "enum Color:\n  case Red, Green, Blue\n\ntrait Show[A]:\n  def show(a: A): String\n\ngiven Show[Int] with\n  def show(a: Int): String = a.toString\n\nextension (s: String)\n  def shout: String = s.toUpperCase + \"!\"\n\n@main def run(): Unit =\n  val xs = List(1, 2, 3)\n  for x <- xs do println(x)\n", Scala},
		{"Scala/snippet for-comp", "val result = for {\n  user  <- findUser(id)\n  order <- findOrders(user)\n} yield order.total\n", Scala},
		{"Scala/sbt", "name := \"demo\"\nscalaVersion := \"3.4.2\"\nlibraryDependencies += \"org.typelevel\" %% \"cats-core\" % \"2.10.0\"\n", Scala},
		{"Scala/2 object braces", "object Main {\n  def main(args: Array[String]): Unit = {\n    val nums = Seq(1, 2, 3).map(_ * 2)\n    lazy val total = nums.sum\n    println(s\"total=$total\")\n  }\n}\n", Scala},
		{"Kotlin/hello", "fun main() {\n    println(\"Hi\")\n}\n", Kotlin},
		{"Scala/hello", "object Hi {\n  def main(args: Array[String]): Unit = println(\"hi\")\n}\n", Scala},
		{"Groovy/hello", "println \"hi\"\n", Groovy},
		{"Java/hello", "class Hi {\n  public static void main(String[] args) {\n    System.out.println(\"hi\");\n  }\n}\n", Java},
		{"Java/comment trap", "// fun foo() => val x = case class\nint total = 0;\nfor (int i = 0; i < n; i++) {\n    total += i;\n}\n", Java},
		{"Kotlin/object only", "object Registry {\n    private val map = HashMap<String, Int>()\n    fun put(k: String, v: Int) { map[k] = v }\n}\n", Kotlin},
		{"Scala/private def", "class Repo(db: Db) {\n  private def load(id: Long) = db.get(id)\n  override def toString = \"Repo\"\n}\n", Scala},
		{"Java/WebFilter /* in string", "@WebFilter(\"/*\")\npublic class AuthFilter implements Filter {\n    @Override\n    public void doFilter(ServletRequest req, ServletResponse res, FilterChain chain) throws IOException {\n        chain.doFilter(req, res);\n    }\n}\n", Java},
		{"Groovy/shebang", "#!/usr/bin/env groovy\nprintln args.size()\n", Groovy},
		{"Scala/scala-cli directive", "//> using scala 3.5\n@main def hi() = println(\"hi\")\n", Scala},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Source(c.code)
			if got.Language != c.want || !got.Confident {
				t.Errorf("Source() = %+v, want {Language:%s Confident:true}", got, c.want)
			}
		})
	}
}

func TestSourceUnknown(t *testing.T) {
	for _, code := range []string{"", "x = 1", "// just a comment"} {
		if got := Source(code); got.Language != Unknown || got.Confident {
			t.Errorf("Source(%q) = %+v, want Unknown", code, got)
		}
	}
}

func TestStripNonCodeKeepsCodeAfterSlashStarInString(t *testing.T) {
	code := "@WebFilter(\"/*\")\npublic class F {}\n"
	want := "@WebFilter(\"  \")\npublic class F {}\n"
	if got := stripNonCode(code); got != want {
		t.Errorf("stripNonCode() = %q, want %q", got, want)
	}
}
