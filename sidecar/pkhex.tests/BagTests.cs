using System.Linq;
using PKHeX.Core;
using Xunit;

// Bag.Read/Bag.Write pin the actual mutation technique the /bag endpoints
// use: PKHeX's pouch/item objects are mutated in place (SaveFile.Inventory
// has no setter, see Bag.cs), so what matters is that a change survives a
// real sav.Write() and re-parse, not just that the in-memory objects took
// the assignment.
public class BagTests
{
    static string CorpusRoot()
    {
        var dir = AppContext.BaseDirectory;
        for (int i = 0; i < 8 && dir is not null; i++)
        {
            var candidate = Path.Combine(dir, "development", "ai_context");
            if (Directory.Exists(candidate)) return candidate;
            dir = Path.GetDirectoryName(dir);
        }
        throw new DirectoryNotFoundException("corpus not found");
    }

    static SaveFile LoadDiamond()
    {
        var path = Path.Combine(CorpusRoot(), "RoCs-PC", "09 - Gen IV - DPPt Collection", "Save Data", "Pokemon Diamond.sav");
        var raw = File.ReadAllBytes(path);
        Assert.True(SaveUtil.TryGetSaveFile(raw, out var sav, "diamond.sav"));
        return sav!;
    }

    // Reading twice must report the same held items, a basic sanity check
    // before trusting a write round trip.
    [Fact]
    public void ReadIsStable()
    {
        var sav = LoadDiamond();
        var first = Bag.Read(sav);
        var second = Bag.Read(sav);
        Assert.Equal(first.Length, second.Length);
        for (int i = 0; i < first.Length; i++)
        {
            Assert.Equal(first[i].Type, second[i].Type);
            Assert.Equal(first[i].Items.Length, second[i].Items.Length);
        }
    }

    // Writing a new item into the Items pouch, re-serializing the save, and
    // re-parsing it from scratch must show that exact item, proof the
    // mutation actually reaches the raw save bytes, not just the live
    // objects Bag.Write touched.
    [Fact]
    public void AWrittenItemSurvivesAWriteAndReRead()
    {
        var sav = LoadDiamond();
        var pouches = Bag.Read(sav);
        var itemsPouch = Array.Find(pouches, p => p.Type == "Items");
        Assert.NotNull(itemsPouch);

        // Potion (index 17 in Gen 4's item table) x 42, in the first slot,
        // whatever was already there is intentionally discarded.
        var newItems = new BagItem[] { new(17, null, 42) };
        Bag.Write(sav, new[] { itemsPouch! with { Items = newItems } });

        var written = sav.Write();
        Assert.True(SaveUtil.TryGetSaveFile(written.ToArray(), out var reloaded, "diamond.sav"));
        var reread = Bag.Read(reloaded!);
        var reloadedPouch = Array.Find(reread, p => p.Type == "Items");
        Assert.NotNull(reloadedPouch);
        Assert.Single(reloadedPouch!.Items);
        Assert.Equal(17, reloadedPouch.Items[0].Index);
        Assert.Equal(42, reloadedPouch.Items[0].Count);
    }

    // Clearing a pouch down to fewer items than it started with must blank
    // the remaining slots, not leave stale entries behind.
    [Fact]
    public void WriteClearsSlotsNotInTheRequest()
    {
        var sav = LoadDiamond();
        var pouches = Bag.Read(sav);
        var keyItems = Array.Find(pouches, p => p.Type == "KeyItems");
        Assert.NotNull(keyItems);
        Assert.True(keyItems!.Items.Length > 0, "fixture save should hold at least one key item to clear");

        Bag.Write(sav, new[] { keyItems with { Items = Array.Empty<BagItem>() } });

        var written = sav.Write();
        Assert.True(SaveUtil.TryGetSaveFile(written.ToArray(), out var reloaded, "diamond.sav"));
        var reread = Bag.Read(reloaded!);
        var reloadedPouch = Array.Find(reread, p => p.Type == "KeyItems");
        Assert.NotNull(reloadedPouch);
        Assert.Empty(reloadedPouch!.Items);
    }

    // A pouch not named in the write request must be left exactly as it was.
    [Fact]
    public void WriteLeavesUnmentionedPouchesAlone()
    {
        var sav = LoadDiamond();
        var before = Bag.Read(sav);
        var ballsBefore = Array.Find(before, p => p.Type == "Balls");

        Bag.Write(sav, Array.Empty<BagPouch>());

        var after = Bag.Read(sav);
        var ballsAfter = Array.Find(after, p => p.Type == "Balls");
        Assert.Equal(ballsBefore?.Items.Length, ballsAfter?.Items.Length);
    }

    // A count beyond the pouch's own MaxCount must be rejected rather than
    // silently written, the boundary CheckEVs/CheckIVs already pin for
    // Legality.cs, applied here to the bag's own cap.
    [Fact]
    public void AnOutOfRangeCountIsRejected()
    {
        var sav = LoadDiamond();
        var pouches = Bag.Read(sav);
        var itemsPouch = Array.Find(pouches, p => p.Type == "Items");
        Assert.NotNull(itemsPouch);

        var tooMany = new BagItem[] { new(17, null, itemsPouch!.MaxCount + 1) };
        Assert.Throws<ArgumentException>(() => Bag.Write(sav, new[] { itemsPouch with { Items = tooMany } }));
    }

    // Writing back exactly what Read reported, across every main-series
    // generation the corpus covers, not just Diamond, must round-trip
    // through a real sav.Write() unchanged. This is the CopyTo fix's actual
    // regression guard: every generation's PlayerBag subtype needs its own
    // CopyTo to fire, and only a real corpus save per generation proves it
    // does rather than assuming Diamond's shape generalizes.
    [Theory]
    [InlineData("Pokemon Crystal.sav")]      // gen2
    [InlineData("Pokemon Ruby.sav")]         // gen3 (non-FRLG pouch capacities)
    [InlineData("Pokemon Fire Red.sav")]     // gen3 FRLG
    [InlineData("Pokemon Platinum.sav")]     // gen4
    [InlineData("Pokemon Black.sav")]        // gen5
    [InlineData("Pokemon Black 2.sav")]      // gen5 (separate PlayerBag subtype)
    public void UnchangedPouchesRoundTripThroughAWrite(string filename)
    {
        var root = CorpusRoot();
        var match = Directory.EnumerateFiles(root, filename, SearchOption.AllDirectories).FirstOrDefault();
        Assert.True(match is not null, $"fixture {filename} not found in corpus");
        var raw = File.ReadAllBytes(match!);
        Assert.True(SaveUtil.TryGetSaveFile(raw, out var sav, filename));

        var before = Bag.Read(sav!);
        Bag.Write(sav!, before);
        var written = sav!.Write();

        Assert.True(SaveUtil.TryGetSaveFile(written.ToArray(), out var reloaded, filename));
        var after = Bag.Read(reloaded!);

        Assert.Equal(before.Length, after.Length);
        for (int i = 0; i < before.Length; i++)
        {
            Assert.Equal(before[i].Type, after[i].Type);
            Assert.Equal(before[i].Items.Length, after[i].Items.Length);
            for (int j = 0; j < before[i].Items.Length; j++)
            {
                Assert.Equal(before[i].Items[j].Index, after[i].Items[j].Index);
                Assert.Equal(before[i].Items[j].Count, after[i].Items[j].Count);
            }
        }
    }
}
