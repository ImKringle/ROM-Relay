using System.Text.Json.Serialization;
using PKHeX.Core;

// Trainer data read out of a save. This file holds every generation-specific
// decision so Program.cs stays a list of endpoints.
//
// The sidecar, not the Go app and not the browser, decides what a save can and
// cannot report, because it is the only component that knows the game. Anything
// a game does not record comes back as a note naming the reason, never as a zero.
public static class TrainerReader
{
    // GameCube saves parse into garbage WITHOUT THROWING: Colosseum yields
    // OT="禁辏杨̾Ⅴ栤篐ㆠꋙ蝭" and money 2,077,241,760 against a MaxMoney of
    // 9,999,999. Silent garbage is worse than a crash, so a save that fails
    // either check contributes identity only.
    //
    // Money is uint and MaxMoney is int; both widen to long, so this compiles.
    public static bool LooksTrustworthy(SaveFile sav)
    {
        if (sav.Money > sav.MaxMoney)
            return false;
        return IsPrintable(sav.OT);
    }

    // Deliberately narrow: rejects control characters and the replacement
    // character, not "non-Latin", a Japanese save's OT is legitimately
    // non-ASCII and must pass. The Colosseum garbage is caught by the money
    // check, which is unambiguous; this is the second line.
    static bool IsPrintable(string s)
    {
        if (string.IsNullOrWhiteSpace(s))
            return false;
        foreach (var c in s)
        {
            if (char.IsControl(c) || c == '�')
                return false;
        }
        return true;
    }

    public static TrainerDTO Read(SaveFile sav, byte[] raw)
    {
        var notes = new Dictionary<string, string>();
        bool trusted = LooksTrustworthy(sav);
        if (!trusted)
        {
            // Identity only. Money, progress and play time are all garbage on a
            // save that fails the gate, and a wrong number is worse than none.
            notes["trainer"] = "ROM Relay cannot read trainer data from GameCube saves.";
            return new TrainerDTO(
                Ot: sav.OT ?? "", Tid: (int)sav.DisplayTID, Sid: (int)sav.DisplaySID,
                Gender: sav.Gender, Language: sav.Language,
                PlayedMinutes: 0, Money: 0, MoneyMax: sav.MaxMoney,
                Progress: null, HallOfFame: null, Trusted: false, Notes: notes);
        }
        return new TrainerDTO(
            Ot: sav.OT ?? "", Tid: (int)sav.DisplayTID, Sid: (int)sav.DisplaySID,
            Gender: sav.Gender, Language: sav.Language,
            PlayedMinutes: (sav.PlayedHours * 60) + sav.PlayedMinutes,
            Money: sav.Money, MoneyMax: sav.MaxMoney,
            Progress: ReadProgress(sav, notes),
            HallOfFame: ReadHallOfFame(sav, raw, notes),
            Trusted: true, Notes: notes);
    }

    // Badges are BITFIELDS, not counts, the count is the population count. The
    // accessor differs per generation and there is no interface to lean on:
    // SAV4.Badges is a byte (8), and gen 5 reaches them through SAV5.Misc (not
    // Blocks.Misc). Gen 7 has no badges at all.
    //
    // HGSS is the odd one out: despite its name, SAV4HGSS.Badges16 is NOT a
    // combined 16-bit value, it is one more byte (General[Trainer1+0x1F], Kanto's
    // 8) sitting beside the base SAV4.Badges byte (General[Trainer1+0x1A],
    // Johto's 8). Reading Badges16 alone silently drops Johto and reports at
    // most 8/16 for a save with every badge.
    public static ProgressDTO? ReadProgress(SaveFile sav, Dictionary<string, string> notes)
    {
        switch (sav)
        {
            case SAV1 g1:
                return Badges(g1.Badges, 8);
            case SAV2 g2:
                return Badges(g2.Badges, 16);
            case SAV3 g3:
                return Badges(g3.Badges, 8);
            case SAV4HGSS hgss:
                // Johto in the low byte, Kanto in the high byte: see the doc
                // comment above for why this isn't just hgss.Badges16.
                return Badges(hgss.Badges | (hgss.Badges16 << 8), 16);
            case SAV4 g4:
                return Badges(g4.Badges, 8);
            case SAV5 g5:
                return Badges(g5.Misc.Badges, 8);
            case SAV6 g6:
                return Badges(g6.Badges, 8);
            case SAV7 g7:
                notes["progress"] = "Alola has island trials, not gyms, trials shown instead of badges.";
                return Trials(g7);
            default:
                return null;
        }
    }

    // Masks to max bits before counting: a bitfield may carry unrelated high
    // bits, and a 9/8 badge count is a bug the mask prevents.
    static ProgressDTO Badges(int flags, int max) =>
        new("badges", System.Numerics.BitOperations.PopCount((uint)flags & ((1u << max) - 1)), max);

    static ProgressDTO Trials(SAV7 g7)
    {
        var stamps = g7.Misc.Stamps;
        var trials = new[]
        {
            Stamp7.MelemeleTrialCompletion, Stamp7.AkalaTrialCompletion,
            Stamp7.UlaulaTrialCompletion, Stamp7.PoniTrialCompletion,
        };
        int count = 0;
        foreach (var s in trials)
            if ((stamps & (1u << (int)s)) != 0) count++;
        return new("trials", count, 4);
    }

    // Ported from development/pkhex-spike/Program.cs, which proved each of these
    // against real completed saves.
    static HallOfFameDTO? ReadHallOfFame(SaveFile sav, byte[] raw, Dictionary<string, string> notes)
    {
        switch (sav)
        {
            case SAV1 g1: return Gen1(g1);
            case SAV2:
                notes["hall_of_fame"] = "This game does not record Elite Four data.";
                return null;
            case SAV3 g3: return Gen3(g3, notes);
            case SAV4 g4: return Gen4(g4, raw);
            case SAV5 g5: return Gen5(g5, notes);
            case SAV6XY xy: return Gen6(xy.Blocks.HallOfFame, sav);
            case SAV6AO ao: return Gen6(ao.Blocks.HallOfFame, sav);
            case SAV7 g7: return Gen7(g7, notes);
            default: return null;
        }
    }

    static HallOfFameDTO Gen1(SAV1 g1)
    {
        var teams = new List<HofTeamDTO>();
        for (int t = 0; t < HallOfFameReader1.TeamCount; t++)
        {
            int count = g1.HallOfFame.GetTeamMemberCount(t);
            if (count == 0) continue;
            var members = new List<HofMemberDTO>();
            for (int i = 0; i < count; i++)
            {
                var e = g1.HallOfFame.GetEntity(t, i);
                if (e.Species == 0) continue;
                members.Add(new HofMemberDTO(e.Species, e.Level, ""));
            }
            if (members.Count > 0) teams.Add(new HofTeamDTO(members));
        }
        // HallOfFameCount is authoritative for the count; the team list is not.
        // The spike counted 50 non-empty teams on a save whose HallOfFameCount is
        // 1, because species 0 was read as filled.
        return new HallOfFameDTO(g1.HallOfFameCount, teams);
    }

    static HallOfFameDTO Gen3(SAV3 g3, Dictionary<string, string> notes)
    {
        var data = g3.GetHallOfFameData();
        var teams = new List<HofTeamDTO>();
        for (int i = 0; i < data.Length / HallFame3Entry.SIZE; i++)
        {
            var entry = new HallFame3Entry(data.AsMemory(i * HallFame3Entry.SIZE, HallFame3Entry.SIZE), false);
            if (entry.GetMember(0).Species == 0) continue;
            var members = new List<HofMemberDTO>();
            for (int m = 0; m < 6; m++)
            {
                var mem = entry.GetMember(m);
                if (mem.Species != 0) members.Add(new HofMemberDTO(mem.Species, mem.Level, ""));
            }
            teams.Add(new HofTeamDTO(members));
        }
        // Derived, not counted: gen 3 keeps the last 50 visits and no lifetime
        // total, so the number stops rising for a player past the cap.
        notes["hall_of_fame_clears"] = "This game keeps the last 50 Hall of Fame visits, not a lifetime count.";
        return new HallOfFameDTO(teams.Count, teams);
    }

    // Dendou4 is an ORPHAN class, PKHeX.Core defines it but nothing constructs
    // it, and the block sits OUTSIDE the General partition (DP/Pt at 0x20000,
    // HGSS at 0x23000), which is why slicing General finds nothing. It is located
    // by scanning the raw file on 0x1000 boundaries and accepting only a block
    // whose own magic/size/checksum validate.
    static HallOfFameDTO? Gen4(SAV4 g4, byte[] raw)
    {
        Dendou4? best = null;
        for (int off = 0; off + 0x2AC0 <= raw.Length; off += 0x1000)
        {
            try
            {
                var hof = new Dendou4(raw.AsMemory(off, 0x2AC0), g4.Language);
                if (hof.IsValid && (best is null || hof.ClearCount > best.ClearCount))
                    best = hof;
            }
            catch { /* not a Dendou4 block at this offset */ }
        }
        if (best is null) return null;
        var teams = new List<HofTeamDTO>();
        var rec = best[0];
        var members = new List<HofMemberDTO>();
        for (int i = 0; i < 6; i++)
        {
            var m = rec[i];
            if (m.Species != 0) members.Add(new HofMemberDTO(m.Species, m.Level, ""));
        }
        if (members.Count > 0) teams.Add(new HofTeamDTO(members));
        return new HallOfFameDTO((int)best.ClearCount, teams);
    }

    // The layout is ours (PKHeX gives the block, not a parser), so it is sanity-
    // gated rather than trusted: there is no checksum to lean on.
    //   member (0x3C): species u16 | level u16 | PID u32 | ? u32
    //                  nickname UTF-16LE @0x0C (FFFF-terminated)
    //   team  (0x16C): 6 members + 4;  block (0x155C): 15 teams + 8
    static HallOfFameDTO? Gen5(SAV5 g5, Dictionary<string, string> notes)
    {
        const int MemberSize = 0x3C, PerTeam = 6, TeamSize = (MemberSize * PerTeam) + 4;
        var block = g5.HallOfFame1.Span;

        // All-00 or all-FF means never entered, which is not "zero clears".
        bool blank = true;
        foreach (var b in block)
        {
            if (b != 0x00 && b != 0xFF) { blank = false; break; }
        }
        if (blank) return null;

        var teams = new List<HofTeamDTO>();
        for (int t = 0; t < 15; t++)
        {
            var team = block.Slice(t * TeamSize, TeamSize);
            var members = new List<HofMemberDTO>();
            for (int m = 0; m < PerTeam; m++)
            {
                var e = team.Slice(m * MemberSize, MemberSize);
                int species = e[0] | (e[1] << 8);
                int level = e[2] | (e[3] << 8);
                if (species is 0 or > 649 || level is < 1 or > 100) continue;
                var nick = StringConverter5.GetString(e.Slice(0x0C, 0x16));
                members.Add(new HofMemberDTO(species, level, nick ?? ""));
            }
            if (members.Count == 0) continue;
            teams.Add(new HofTeamDTO(members));
        }
        if (teams.Count == 0) return null;
        notes["hall_of_fame_clears"] = "This game keeps the last 15 Hall of Fame visits, not a lifetime count.";
        return new HallOfFameDTO(teams.Count, teams);
    }

    static HallOfFameDTO? Gen6(HallOfFame6 h6, SaveFile sav)
    {
        int filled = 0;
        for (int t = 0; t < HallOfFame6.Entries; t++)
            if (new HallFame6Index(h6.GetEntry(t)[^4..]).HasData) filled++;
        if (filled == 0) return null;
        var members = new List<HofMemberDTO>();
        for (int m = 0; m < HallOfFame6.PokeCount; m++)
        {
            var ent = new HallFame6Entity(h6.GetEntity(0, m), sav.Language);
            if (ent.Species != 0) members.Add(new HofMemberDTO(ent.Species, 0, ""));
        }
        var idx = new HallFame6Index(h6.GetEntry(0)[^4..]);
        return new HallOfFameDTO((int)idx.ClearIndex, new List<HofTeamDTO> { new(members) });
    }

    // Gen 7 records the team but no count and no levels, so Clears is null (which
    // is not zero) and every level is 0.
    static HallOfFameDTO? Gen7(SAV7 g7, Dictionary<string, string> notes)
    {
        var members = new List<HofMemberDTO>();
        for (int i = 0; i < 12; i++)
        {
            var species = g7.EventWork.Fame.GetEntry(i);
            if (species != 0) members.Add(new HofMemberDTO(species, 0, ""));
        }
        if (members.Count == 0) return null;
        notes["hall_of_fame_clears"] = "This game records the Hall of Fame team but not how many times you have won.";
        return new HallOfFameDTO(null, new List<HofTeamDTO> { new(members) });
    }
}

public record ProgressDTO(string Kind, int Count, int Max);

// Clears is int? on purpose: null means "this game does not count", which is
// not zero. Gen 7 records teams with no count, so it sends teams and a null
// count plus its note.
public record HallOfFameDTO(int? Clears, List<HofTeamDTO> Teams);

public record HofTeamDTO(List<HofMemberDTO> Members);

public record HofMemberDTO(int Species, int Level, string Nickname);

public record TrainerDTO(
    string Ot, int Tid, int Sid, int Gender, int Language,
    [property: JsonPropertyName("played_minutes")] int PlayedMinutes,
    uint Money,
    [property: JsonPropertyName("money_max")] int MoneyMax,
    ProgressDTO? Progress,
    [property: JsonPropertyName("hall_of_fame")] HallOfFameDTO? HallOfFame,
    bool Trusted,
    Dictionary<string, string> Notes);
