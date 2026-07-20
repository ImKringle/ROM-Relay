using PKHeX.Core;

// Pins the QA fix "Pokemon Creator has no validation." Legality.Violations is
// deliberately narrow (ability + IV/EV range only, see its own comment for
// why full LegalityAnalysis can't be used here), so the critical regression
// to guard is that an ordinary from-scratch creation, exactly what the
// Creator's default flow builds, passes clean, not just that an illegal one
// fails.
public class LegalityTests
{
    static PK9 BlankBulbasaur()
    {
        var pkm = (PK9)EntityBlank.GetBlank(EntityContext.Gen9);
        pkm.Species = 1; // Bulbasaur
        pkm.CurrentLevel = 5;
        pkm.Ability = 65; // Overgrow, one of Bulbasaur's real abilities
        pkm.RefreshChecksum();
        return pkm;
    }

    // The regression this whole feature exists to avoid: the Creator's own
    // default output (species, level, a real ability, zeroed IVs/EVs) must
    // not be rejected. A hard gate that blocks its own product's default
    // output is worse than no gate at all.
    [Fact]
    public void AnOrdinaryFromScratchCreationIsLegal()
    {
        Assert.Null(Legality.Violations(BlankBulbasaur()));
    }

    [Fact]
    public void AnAbilityTheSpeciesCannotHaveIsRejected()
    {
        var pkm = BlankBulbasaur();
        pkm.Ability = 9999; // not one of Bulbasaur's abilities
        pkm.RefreshChecksum();

        var report = Legality.Violations(pkm);

        Assert.NotNull(report);
        Assert.Contains(report!, line => line.Contains("Ability"));
    }

    // Per-stat IV/EV values can't actually go out of range through the
    // normal object model (PKM's own setters clamp them, see CheckIVs'
    // comment in Legality.cs), so the meaningful stat check to pin is the
    // cross-field one no single setter can enforce: the 510 EV total cap.
    [Fact]
    public void EVsOverTheTotalCapAreRejected()
    {
        var pkm = BlankBulbasaur();
        pkm.EV_HP = 252;
        pkm.EV_ATK = 252;
        pkm.EV_DEF = 252; // 756 total, over the 510 cap
        pkm.RefreshChecksum();

        var report = Legality.Violations(pkm);

        Assert.NotNull(report);
        Assert.Contains(report!, line => line.Contains("Total EVs"));
    }

    // Gen 1 has no abilities at all, the ability check must not misfire
    // (e.g. treating "no abilities" as "any ability is invalid").
    [Fact]
    public void Gen1HasNoAbilityCheck()
    {
        var pkm = EntityBlank.GetBlank(EntityContext.Gen1);
        pkm.Species = 1;
        pkm.CurrentLevel = 5;
        pkm.RefreshChecksum();

        Assert.Null(Legality.Violations(pkm));
    }
}
