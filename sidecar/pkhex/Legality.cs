using System.Linq;
using PKHeX.Core;

// Wraps a targeted subset of PKHeX's legality data for the sidecar's
// /create and /edit hard-block. A separate file/class rather than a
// Program.cs local function, so pkhex.tests can exercise it directly
// against known entities instead of only through an HTTP round-trip.
//
// This deliberately does NOT use PKHeX.Core's full LegalityAnalysis: that
// checks "could this have been legitimately obtained in a real game" (a
// matching encounter, met location, OT/language/nickname consistency, a
// Pokémon HOME transfer tracker, and so on), which a from-scratch created
// Pokémon can never satisfy, it has no game-original encounter by design,
// the same way PKHeX's own "create a new Pokémon in a box" feature produces
// one. Measured empirically: a bare EntityBlank + species + level fails full
// LegalityAnalysis with 11 violations before a single field is even set,
// which would reject 100% of creations rather than just illegal ones. This
// checks only the parts of an entity that are legal or not independent of
// any encounter: its own ability and its own stat values.
public static class Legality
{
    // Violations returns null when entity passes every check here, or the
    // list of violations when it doesn't. Enforced as a hard block (product
    // decision, not a warning).
    public static string[]? Violations(PKM entity)
    {
        var violations = new List<string>();
        CheckAbility(entity, violations);
        CheckIVs(entity, violations);
        CheckEVs(entity, violations);
        return violations.Count > 0 ? violations.ToArray() : null;
    }

    // CheckAbility confirms entity.Ability is one of its own species/form's
    // real ability slots (GetIndexOfAbility returns -1 for anything else).
    // Gen 1-2 formats have no abilities at all (AbilityCount is 0 there),
    // so there is nothing to check.
    static void CheckAbility(PKM entity, List<string> violations)
    {
        if (entity.PersonalInfo is not IPersonalAbility info || info.AbilityCount == 0)
            return;
        if (info.GetIndexOfAbility(entity.Ability) < 0)
            violations.Add($"Ability {entity.Ability} is not one of this species' legal abilities.");
    }

    // Per-stat IV/EV range checks are a defensive backstop, not the primary
    // gate: PKM's own IV_*/EV_* setters already clamp to [0,31]/[0,252] at
    // assignment time (verified empirically, setting IV_HP = 99 reads back
    // as 31), so an out-of-range single stat can't actually reach here via
    // the normal object model. The EV *total* check right after this one is
    // the check that matters in practice: three individually-valid EVs
    // (252 each) can still sum past the 510 cap, which no per-stat setter
    // can catch on its own.
    static void CheckIVs(PKM entity, List<string> violations)
    {
        foreach (var (name, value) in new[]
                 {
                     ("HP", entity.IV_HP), ("Attack", entity.IV_ATK), ("Defense", entity.IV_DEF),
                     ("Sp. Atk", entity.IV_SPA), ("Sp. Def", entity.IV_SPD), ("Speed", entity.IV_SPE),
                 })
        {
            if (value is < 0 or > 31)
                violations.Add($"{name} IV {value} is outside the legal 0-31 range.");
        }
    }

    static void CheckEVs(PKM entity, List<string> violations)
    {
        var stats = new[]
        {
            ("HP", entity.EV_HP), ("Attack", entity.EV_ATK), ("Defense", entity.EV_DEF),
            ("Sp. Atk", entity.EV_SPA), ("Sp. Def", entity.EV_SPD), ("Speed", entity.EV_SPE),
        };
        foreach (var (name, value) in stats)
        {
            if (value is < 0 or > 252)
                violations.Add($"{name} EV {value} is outside the legal 0-252 range.");
        }
        var total = stats.Sum(s => s.Item2);
        if (total > 510)
            violations.Add($"Total EVs ({total}) exceed the legal maximum of 510.");
    }
}
