using PKHeX.Core;
using Xunit;

// The corpus is the test: 53 real saves across every generation. Synthetic saves
// would not have caught any of the bugs this work found, a Colosseum save that
// parses to garbage without throwing, an XD .gci that throws out of
// TryGetSaveFile, or PKHeX reading past a save's dex range.
public class CorpusTests
{
    static string? CorpusRoot()
    {
        // Walk up from the test binary until the repo root's development/ai_context.
        var dir = AppContext.BaseDirectory;
        for (int i = 0; i < 8 && dir is not null; i++)
        {
            var candidate = Path.Combine(dir, "development", "ai_context");
            if (Directory.Exists(candidate)) return candidate;
            dir = Path.GetDirectoryName(dir);
        }
        return null;
    }

    static IEnumerable<string> Saves()
    {
        var root = CorpusRoot();
        if (root is null) yield break;
        foreach (var f in Directory.EnumerateFiles(root, "*", SearchOption.AllDirectories))
        {
            if (f.Contains(".git")) continue;
            var ext = Path.GetExtension(f).ToLowerInvariant();
            if (ext is ".sav" or ".dsv" or ".gci" or ".bin" or ".srm" or ".dat")
                yield return f;
        }
    }

    public static TheoryData<string> AllSaves()
    {
        var data = new TheoryData<string>();
        foreach (var f in Saves()) data.Add(f);
        // TheoryData cannot be empty or xunit fails the discovery; the corpus
        // presence itself is asserted by TheCorpusIsActuallyThere.
        if (data.Count == 0) data.Add("no-corpus");
        return data;
    }

    // A green corpus suite that found no files is exactly the vacuous-test shape
    // this project has already shipped twice.
    [Fact]
    public void TheCorpusIsActuallyThere()
    {
        Assert.NotEmpty(Saves());
    }

    // No save in the corpus may throw out of the read path. An XD .gci used to
    // raise ArgumentOutOfRangeException out of SaveUtil.TryGetSaveFile, which the
    // sidecar surfaced as a 500.
    [Theory]
    [MemberData(nameof(AllSaves))]
    public void NoSaveThrows(string path)
    {
        if (path == "no-corpus") return;
        var raw = File.ReadAllBytes(path);
        SaveFile? sav = null;
        var ex = Record.Exception(() =>
        {
            try
            {
                SaveUtil.TryGetSaveFile(raw, out sav, path);
            }
            catch
            {
                // TryGetSaveFile throwing is a known PKHeX behaviour the sidecar
                // guards with its own try/catch (a 400, not a 500). What must
                // never throw is OUR read path below.
                sav = null;
            }
        });
        Assert.Null(ex);
        if (sav is null) return;
        Assert.Null(Record.Exception(() => TrainerReader.Read(sav, raw)));
    }

    // A trusted save has sane money and a printable OT. This is the gate that
    // stops Colosseum's OT="禁辏杨̾Ⅴ栤篐ㆠꋙ蝭" / money 2,077,241,760 from being
    // written to the database as though it were real.
    [Theory]
    [MemberData(nameof(AllSaves))]
    public void TrustedSavesAreSane(string path)
    {
        if (path == "no-corpus") return;
        var raw = File.ReadAllBytes(path);
        SaveFile? sav;
        try
        {
            if (!SaveUtil.TryGetSaveFile(raw, out sav, path) || sav is null) return;
        }
        catch
        {
            return;
        }
        var t = TrainerReader.Read(sav, raw);
        if (!t.Trusted) return;
        Assert.True(t.Money <= (uint)t.MoneyMax, $"{path}: money {t.Money} > max {t.MoneyMax}");
        Assert.False(string.IsNullOrWhiteSpace(t.Ot), $"{path}: trusted save with a blank OT");
        if (t.Progress is not null)
            Assert.InRange(t.Progress.Count, 0, t.Progress.Max);
    }
}
