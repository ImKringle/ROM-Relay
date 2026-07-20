using System.Text.Json.Serialization;
using PKHeX.Core;

// ROM Relay PKHeX sidecar: a localhost-only service the Go backend calls to parse,
// inject, and edit Pokémon save data. PKHeX.Core is .NET-only, so it lives here
// rather than in the Go application. Endpoints:
//   GET  /health  -> readiness probe
//   POST /parse   -> raw save bytes in the body, parsed Pokémon JSON out
//   POST /inject  -> a stored Pokémon's raw bytes + a destination save -> that
//                    save with the Pokémon written into a chosen box slot
//   POST /edit    -> a stored Pokémon's raw bytes + field changes -> the
//                    edited entity's raw bytes
//   POST /bag/read  -> a save's raw bytes -> its item pouches
//   POST /bag/write -> a save's raw bytes + pouch contents -> the save
//                      with its bag rewritten
//
// /inject and /edit are the sidecar's first *write* paths, /parse only reads.

var builder = WebApplication.CreateBuilder(args);
builder.Services.ConfigureHttpJsonOptions(o =>
{
    // snake_case gives the Go client a clean, predictable contract.
    o.SerializerOptions.PropertyNamingPolicy = System.Text.Json.JsonNamingPolicy.SnakeCaseLower;
    o.SerializerOptions.DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull;
});
var app = builder.Build();

app.MapGet("/health", () => Results.Json(new { status = "ok", library = "PKHeX.Core" }));

// The National Pokédex: number -> name. Drives the Living Dex grid (including
// species the user does not yet own).
app.MapGet("/species", () =>
{
    var strings = GameInfo.GetStrings("en");
    var list = new List<SpeciesEntry>(strings.Species.Count);
    for (ushort i = 1; i < strings.Species.Count; i++)
    {
        var name = strings.Species[i];
        if (!string.IsNullOrWhiteSpace(name))
            list.Add(new SpeciesEntry(i, name));
    }
    return Results.Json(new { count = list.Count, species = list });
});

// Ability/held-item/move name lists, the same GameStrings tables ApplyEdits
// already resolves display names against (RequireIndex), exposed so a client
// can offer a search dropdown that is guaranteed to only ever submit a name
// RequireIndex will accept.
app.MapGet("/abilities", () =>
{
    var strings = GameInfo.GetStrings("en");
    var list = new List<NamedEntry>(strings.Ability.Count);
    for (int i = 1; i < strings.Ability.Count; i++)
        if (!string.IsNullOrWhiteSpace(strings.Ability[i]))
            list.Add(new NamedEntry(i, strings.Ability[i]));
    return Results.Json(new { count = list.Count, abilities = list });
});

app.MapGet("/items", () =>
{
    var strings = GameInfo.GetStrings("en");
    var list = new List<NamedEntry>(strings.Item.Count);
    for (int i = 1; i < strings.Item.Count; i++)
        if (!string.IsNullOrWhiteSpace(strings.Item[i]))
            list.Add(new NamedEntry(i, strings.Item[i]));
    return Results.Json(new { count = list.Count, items = list });
});

app.MapGet("/moves", () =>
{
    var strings = GameInfo.GetStrings("en");
    var list = new List<NamedEntry>(strings.Move.Count);
    for (int i = 1; i < strings.Move.Count; i++)
        if (!string.IsNullOrWhiteSpace(strings.Move[i]))
            list.Add(new NamedEntry(i, strings.Move[i]));
    return Results.Json(new { count = list.Count, moves = list });
});

app.MapPost("/parse", async (HttpRequest request) =>
{
    using var ms = new MemoryStream();
    await request.Body.CopyToAsync(ms);
    var raw = ms.ToArray();
    if (raw.Length == 0)
        return Results.BadRequest(new { error = "empty request body" });

    // TryGetSaveFile throws rather than returning false on some inputs, an XD
    // .gci raises ArgumentOutOfRangeException out of SAV3XD.InitializeData. A
    // file we cannot read is the save's problem, not the server's, so it is a
    // 400 either way rather than an unhandled 500.
    SaveFile? sav;
    try
    {
        if (!SaveUtil.TryGetSaveFile(raw, out sav, "upload.sav"))
            return Results.BadRequest(new { error = "unrecognized save file", size = raw.Length });
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"unreadable save file ({e.GetType().Name})", size = raw.Length });
    }
    if (!sav.State.Exportable)
        return Results.BadRequest(new { error = "save file is not readable (empty or corrupt)", size = raw.Length });

    var strings = GameInfo.GetStrings("en");
    var mons = new List<ParsedMon>();

    // The outer try is the last line of defence: a save quirk PKHeX chokes on
    // is that save's problem. This sidecar must answer 4xx, never crash, the
    // Go app treats an unreachable parser as "upload again later".
    try
    {
        for (int i = 0; i < sav.PartyCount; i++)
            AddMon(mons, strings, sav.GetPartySlotAtIndex(i), $"party:{i + 1}");
        for (int box = 0; box < sav.BoxCount; box++)
            for (int slot = 0; slot < sav.BoxSlotCount; slot++)
                AddMon(mons, strings, sav.GetBoxSlotAtIndex(box, slot), $"box:{box + 1}:{slot + 1}");
    }
    catch (Exception e)
    {
        return Results.UnprocessableEntity(new { error = $"could not read the save's Pokémon ({e.GetType().Name})", size = raw.Length });
    }

    // The save's own Pokédex, seen/caught flags per species, drives the true
    // regional Pokédex view (progress within this game, not just Pokémon kept).
    //
    // The loop stops at the save's own MaxSpeciesID, never at the length of the
    // modern species list. A save only stores flags for the species its game
    // knows about, and PKHeX reads past that range without complaining: asking a
    // HeartGold save (max 493) about #1024 returned a bit from unrelated memory,
    // which reported more Pokémon "caught" than were ever "seen".
    var seen = new List<int>();
    var caught = new List<int>();
    var maxSpecies = (int)sav.MaxSpeciesID;
    if (sav.HasPokeDex)
    {
        for (ushort species = 1; species <= maxSpecies && species < strings.Species.Count; species++)
        {
            try
            {
                if (sav.GetSeen(species)) seen.Add(species);
                if (sav.GetCaught(species)) caught.Add(species);
            }
            catch { /* species outside this game's dex range */ }
        }
    }

    try
    {
        return Results.Json(new ParseResult(
            Game: ((GameVersion)sav.Version).ToString(),
            Generation: sav.Generation,
            Trainer: TrainerReader.Read(sav, raw),
            Count: mons.Count,
            // MaxSpecies is what makes a per-game completion figure honest: HeartGold
            // is 180/493, not 180/1025.
            MaxSpecies: maxSpecies,
            Dex: new DexFlags(seen, caught),
            Pokemon: mons,
            // The box grid a save the sidecar reports about is bounded by these two
            // numbers; /parse only ever emits occupied slots (AddMon skips Species==0),
            // so a caller building a full box grid, PokeHome's withdrawal picker,
            // derives the empty slots as "everything between here and BoxCount x
            // BoxSlotCount that this response didn't mention."
            BoxCount: sav.BoxCount,
            BoxSlotCount: sav.BoxSlotCount));
    }
    catch (Exception e)
    {
        return Results.UnprocessableEntity(new { error = $"could not read the save's trainer ({e.GetType().Name})", size = raw.Length });
    }
});

app.MapPost("/inject", async (HttpRequest request) =>
{
    InjectRequest? body;
    try
    {
        body = await request.ReadFromJsonAsync<InjectRequest>();
    }
    catch (Exception)
    {
        return Results.BadRequest(new { error = "invalid request body" });
    }
    if (body is null || string.IsNullOrEmpty(body.Entity) || string.IsNullOrEmpty(body.Save))
        return Results.BadRequest(new { error = "entity and save are required" });

    byte[] entityBytes, saveBytes;
    try
    {
        entityBytes = Convert.FromBase64String(body.Entity);
        saveBytes = Convert.FromBase64String(body.Save);
    }
    catch (FormatException)
    {
        return Results.BadRequest(new { error = "entity and save must be base64" });
    }

    SaveFile? sav;
    try
    {
        if (!SaveUtil.TryGetSaveFile(saveBytes, out sav, "destination.sav"))
            return Results.BadRequest(new { error = "unrecognized destination save", size = saveBytes.Length });
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"unreadable destination save ({e.GetType().Name})", size = saveBytes.Length });
    }
    if (!sav.State.Exportable)
        return Results.BadRequest(new { error = "destination save is not readable (empty or corrupt)" });

    if (body.Box < 1 || body.Box > sav.BoxCount || body.Slot < 1 || body.Slot > sav.BoxSlotCount)
        return Results.BadRequest(new { error = "box/slot is out of range for the destination save" });
    int box = body.Box - 1, slot = body.Slot - 1;

    PKM entity;
    try
    {
        entity = DecodeEntity(entityBytes, body.Generation);
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"could not decode entity ({e.GetType().Name})" });
    }

    // The destination save's occupancy is checked against the save bytes given
    // in this request, not re-fetched, the caller (Go) already loaded the save
    // it wants written into, and re-reading here would just be the same bytes
    // twice. A slot that filled between the client picking it and this request
    // arriving is still caught: that's exactly what this check is for.
    // Overwrite skips this check entirely, the Go side's evict_to_home option
    // already read this slot's occupant out via /parse and stored it before
    // asking to overwrite, so the occupant is not silently discarded.
    if (!body.Overwrite && sav.GetBoxSlotAtIndex(box, slot).Species != 0)
        return Results.Conflict(new { error = "slot occupied" });

    // If the decoded entity isn't already the destination save's native PKM
    // type (a Gen 1 entity going into a Gen 7 save, for instance), PKHeX's own
    // EntityConverter performs the structural conversion, the same machinery
    // real transfer tools use. This sidecar does not implement cross-generation
    // conversion itself; it either works through PKHeX or it 409s.
    if (entity.GetType() != sav.PKMType)
    {
        PKM? converted = EntityConverter.ConvertToType(entity, sav.PKMType, out var conversion);
        if (converted is null || conversion is not (EntityConverterResult.Success
            or EntityConverterResult.SuccessIncompatibleManual or EntityConverterResult.SuccessIncompatibleReflection))
        {
            return Results.Conflict(new { error = $"cannot convert entity to destination format ({conversion})" });
        }
        entity = converted;
    }

    sav.SetBoxSlotAtIndex(entity, box, slot, EntityImportSettings.None);
    var written = sav.Write();
    return Results.Json(new InjectResult(
        Save: Convert.ToBase64String(written.Span),
        Slot: $"box:{body.Box}:{body.Slot}"));
});

app.MapPost("/edit", async (HttpRequest request) =>
{
    EditRequest? body;
    try
    {
        body = await request.ReadFromJsonAsync<EditRequest>();
    }
    catch (Exception)
    {
        return Results.BadRequest(new { error = "invalid request body" });
    }
    if (body is null || string.IsNullOrEmpty(body.Entity))
        return Results.BadRequest(new { error = "entity is required" });

    byte[] entityBytes;
    try
    {
        entityBytes = Convert.FromBase64String(body.Entity);
    }
    catch (FormatException)
    {
        return Results.BadRequest(new { error = "entity must be base64" });
    }

    PKM entity;
    try
    {
        entity = DecodeEntity(entityBytes, body.Generation);
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"could not decode entity ({e.GetType().Name})" });
    }

    var strings = GameInfo.GetStrings("en");
    try
    {
        ApplyEdits(entity, body, strings);
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"could not apply edit ({e.GetType().Name}: {e.Message})" });
    }

    if (Legality.Violations(entity) is { } editViolations)
        return Results.Conflict(new { error = "this edit would make the Pokémon illegal", report = editViolations });

    try
    {
        entity.RefreshChecksum();
        return Results.Json(BuildParsedMon(entity, "", strings));
    }
    catch (Exception e)
    {
        return Results.UnprocessableEntity(new { error = $"could not re-encode the edited entity ({e.GetType().Name})" });
    }
});

app.MapPost("/create", async (HttpRequest request) =>
{
    CreateRequest? body;
    try
    {
        body = await request.ReadFromJsonAsync<CreateRequest>();
    }
    catch (Exception)
    {
        return Results.BadRequest(new { error = "invalid request body" });
    }
    if (body is null || body.Species < 1)
        return Results.BadRequest(new { error = "species is required" });

    // EntityBlank hands back a correctly-typed, zeroed PKM for the requested
    // generation, the same machinery PKHeX's own "new Pokémon" box-slot
    // action uses, rather than this sidecar guessing at per-format defaults.
    PKM entity;
    try
    {
        entity = EntityBlank.GetBlank(ContextForGeneration(body.Generation));
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"could not create a blank Pokémon for generation {body.Generation} ({e.GetType().Name})" });
    }
    entity.Species = (ushort)body.Species;
    entity.CurrentLevel = (byte)Math.Clamp(body.Level < 1 ? 5 : body.Level, 1, 100);

    // Everything past species/level reuses ApplyEdits, a created Pokémon and
    // an edited one set the same fields the same way, so the two paths cannot
    // drift on what a name string is allowed to be.
    var strings = GameInfo.GetStrings("en");
    try
    {
        ApplyEdits(entity, new EditRequest(
            Entity: "", Generation: body.Generation, Nickname: body.Nickname, Level: null,
            Nature: body.Nature, Ability: body.Ability, Gender: body.Gender, Shiny: body.Shiny,
            Form: body.Form, HeldItem: body.HeldItem, Ball: body.Ball,
            OriginalTrainerName: body.OriginalTrainerName, IVs: body.IVs, EVs: body.EVs, Moves: body.Moves),
            strings);
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"could not apply the new Pokémon's fields ({e.GetType().Name}: {e.Message})" });
    }

    if (Legality.Violations(entity) is { } createViolations)
        return Results.Conflict(new { error = "this Pokémon is not legal", report = createViolations });

    try
    {
        entity.RefreshChecksum();
        return Results.Json(BuildParsedMon(entity, "", strings));
    }
    catch (Exception e)
    {
        return Results.UnprocessableEntity(new { error = $"could not encode the new Pokémon ({e.GetType().Name})" });
    }
});

app.MapPost("/bag/read", async (HttpRequest request) =>
{
    BagReadRequest? body;
    try
    {
        body = await request.ReadFromJsonAsync<BagReadRequest>();
    }
    catch (Exception)
    {
        return Results.BadRequest(new { error = "invalid request body" });
    }
    if (body is null || string.IsNullOrEmpty(body.Save))
        return Results.BadRequest(new { error = "save is required" });

    byte[] saveBytes;
    try
    {
        saveBytes = Convert.FromBase64String(body.Save);
    }
    catch (FormatException)
    {
        return Results.BadRequest(new { error = "save must be base64" });
    }

    SaveFile? sav;
    try
    {
        if (!SaveUtil.TryGetSaveFile(saveBytes, out sav, "bag.sav"))
            return Results.BadRequest(new { error = "unrecognized save file", size = saveBytes.Length });
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"unreadable save file ({e.GetType().Name})", size = saveBytes.Length });
    }
    if (!sav.State.Exportable)
        return Results.BadRequest(new { error = "save file is not readable (empty or corrupt)" });

    try
    {
        return Results.Json(new BagReadResult(Bag.Read(sav)));
    }
    catch (Exception e)
    {
        return Results.UnprocessableEntity(new { error = $"could not read this save's bag ({e.GetType().Name})" });
    }
});

app.MapPost("/bag/write", async (HttpRequest request) =>
{
    BagWriteRequest? body;
    try
    {
        body = await request.ReadFromJsonAsync<BagWriteRequest>();
    }
    catch (Exception)
    {
        return Results.BadRequest(new { error = "invalid request body" });
    }
    if (body is null || string.IsNullOrEmpty(body.Save) || body.Pouches is null)
        return Results.BadRequest(new { error = "save and pouches are required" });

    byte[] saveBytes;
    try
    {
        saveBytes = Convert.FromBase64String(body.Save);
    }
    catch (FormatException)
    {
        return Results.BadRequest(new { error = "save must be base64" });
    }

    SaveFile? sav;
    try
    {
        if (!SaveUtil.TryGetSaveFile(saveBytes, out sav, "bag.sav"))
            return Results.BadRequest(new { error = "unrecognized save file", size = saveBytes.Length });
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"unreadable save file ({e.GetType().Name})", size = saveBytes.Length });
    }
    if (!sav.State.Exportable)
        return Results.BadRequest(new { error = "save file is not readable (empty or corrupt)" });

    try
    {
        Bag.Write(sav, body.Pouches);
    }
    catch (Exception e)
    {
        return Results.BadRequest(new { error = $"could not apply the bag changes ({e.GetType().Name}: {e.Message})" });
    }

    var written = sav.Write();
    return Results.Json(new BagWriteResult(Convert.ToBase64String(written.Span)));
});

static void AddMon(List<ParsedMon> outList, GameStrings strings, PKM e, string slot)
{
    if (e.Species == 0)
        return;
    // Every string-table lookup is bounds-checked: older generations produce
    // values the modern tables do not cover (a Gen 2 mon has no Nature, PKHeX
    // synthesizes one that indexed past the table and 500'd the whole parse).
    try
    {
        outList.Add(BuildParsedMon(e, slot, strings));
    }
    catch (Exception)
    {
        // One malformed slot must not sink the save: record what is safe and
        // move on, so 250 good mons are not lost to one bad one.
        string SpeciesName(ushort id) => id < strings.Species.Count ? strings.Species[id] : id.ToString();
        outList.Add(new ParsedMon(
            Slot: slot, SpeciesId: e.Species, Species: SpeciesName(e.Species),
            Nickname: "", Level: 0, Nature: "", Ability: "", Shiny: false, Form: 0,
            Gender: "-", IsEgg: false, HeldItemId: 0, HeldItem: null, Ball: 0, PID: 0,
            OT: "", IVs: new Stats(0, 0, 0, 0, 0, 0), EVs: new Stats(0, 0, 0, 0, 0, 0),
            Moves: Array.Empty<MoveDTO>(), Blob: ""));
    }
}

// BuildParsedMon is the single place a PKM becomes the JSON shape both /parse
// (via AddMon, which catches its exceptions per-slot) and /edit (which lets
// them surface as a 4xx, an edit result that cannot be described honestly
// should fail loudly, not be silently swapped for a blank record) return.
static ParsedMon BuildParsedMon(PKM e, string slot, GameStrings strings)
{
    string SpeciesName(ushort id) => id < strings.Species.Count ? strings.Species[id] : id.ToString();
    string MoveName(ushort id) => id < strings.Move.Count ? strings.Move[id] : id.ToString();

    string gender = e.Gender switch { 0 => "M", 1 => "F", _ => "-" };
    int natureIndex = (int)e.Nature;
    string nature = natureIndex >= 0 && natureIndex < strings.Natures.Count ? strings.Natures[natureIndex] : "";

    var moves = new[] { e.Move1, e.Move2, e.Move3, e.Move4 }
        .Where(m => m != 0)
        .Select(m => new MoveDTO(m, MoveName((ushort)m)))
        .ToArray();

    return new ParsedMon(
        Slot: slot,
        SpeciesId: e.Species,
        Species: SpeciesName(e.Species),
        Nickname: e.Nickname,
        Level: e.CurrentLevel,
        Nature: nature,
        Ability: e.Ability >= 0 && e.Ability < strings.Ability.Count ? strings.Ability[e.Ability] : e.Ability.ToString(),
        Shiny: e.IsShiny,
        Form: e.Form,
        Gender: gender,
        IsEgg: e.IsEgg,
        HeldItemId: e.HeldItem,
        HeldItem: e.HeldItem > 0 && e.HeldItem < strings.Item.Count ? strings.Item[e.HeldItem] : null,
        Ball: e.Ball,
        PID: e.PID,
        OT: e.OriginalTrainerName,
        IVs: new Stats(e.IV_HP, e.IV_ATK, e.IV_DEF, e.IV_SPA, e.IV_SPD, e.IV_SPE),
        EVs: new Stats(e.EV_HP, e.EV_ATK, e.EV_DEF, e.EV_SPA, e.EV_SPD, e.EV_SPE),
        Moves: moves,
        Blob: Convert.ToBase64String(e.Data));
}

// ContextForGeneration maps the plain generation number ROM Relay already
// stores per Pokémon (pokemon_instances.generation, and the request bodies
// below) onto the EntityContext PKHeX needs to disambiguate a byte blob that
// could otherwise match more than one format. It only covers the base
// generations: sub-variants (Gen 7 LGPE, Gen 8 BDSP/PLA) collapse to their
// parent generation already, at the point sav.Generation was first captured,
// this mapping does not need to (and cannot) recover a distinction the stored
// data never kept.
static EntityContext ContextForGeneration(int generation) => generation switch
{
    1 => EntityContext.Gen1,
    2 => EntityContext.Gen2,
    3 => EntityContext.Gen3,
    4 => EntityContext.Gen4,
    5 => EntityContext.Gen5,
    6 => EntityContext.Gen6,
    7 => EntityContext.Gen7,
    8 => EntityContext.Gen8,
    9 => EntityContext.Gen9,
    _ => throw new ArgumentOutOfRangeException(nameof(generation), generation, "unsupported generation"),
};

// DecodeEntity turns raw entity bytes, exactly what /parse handed out as
// ParsedMon.Blob, back into the PKM object they came from. This is the
// inverse of AddMon's Blob field: round-tripping the same bytes, not
// reconstructing a Pokémon from the smaller JSON field set.
static PKM DecodeEntity(byte[] data, int generation) =>
    EntityFormat.GetFromBytes(data, ContextForGeneration(generation)) ?? throw new FormatException("entity bytes did not decode to a Pokémon");

// ApplyEdits mutates entity in place from whichever fields the request set.
// Every field is optional (nullable in EditRequest): a caller changing only
// the nickname sends only nickname. Name-based fields (nature, ability, held
// item) are looked up against the same string tables /parse resolves them
// from, so a client round-trips the exact strings ROM Relay already stores,
// no separate numeric-id contract to keep in sync.
static void ApplyEdits(PKM e, EditRequest body, GameStrings strings)
{
    if (body.Nickname is { } nickname)
    {
        e.Nickname = nickname;
        e.IsNicknamed = true;
    }
    if (body.Level is { } level)
        e.CurrentLevel = (byte)level;
    if (body.Nature is { } nature)
        e.Nature = (Nature)RequireIndex(strings.Natures, nature, "nature");
    if (body.Ability is { } ability)
        e.Ability = RequireIndex(strings.Ability, ability, "ability");
    if (body.Gender is { } gender)
        e.Gender = gender switch { "M" => (byte)0, "F" => (byte)1, "-" => (byte)2, _ => throw new ArgumentException($"unknown gender {gender}") };
    if (body.Shiny is { } shiny)
        CommonEdits.SetIsShiny(e, shiny);
    if (body.Form is { } form)
        e.Form = (byte)form;
    if (body.HeldItem is { } heldItem)
        e.HeldItem = heldItem.Length == 0 ? 0 : RequireIndex(strings.Item, heldItem, "held item");
    if (body.Ball is { } ball)
        e.Ball = (byte)ball;
    if (body.OriginalTrainerName is { } ot)
        e.OriginalTrainerName = ot;
    if (body.IVs is { } ivs)
    {
        if (ivs.Hp is { } v) e.IV_HP = v;
        if (ivs.Atk is { } v2) e.IV_ATK = v2;
        if (ivs.Def is { } v3) e.IV_DEF = v3;
        if (ivs.Spa is { } v4) e.IV_SPA = v4;
        if (ivs.Spd is { } v5) e.IV_SPD = v5;
        if (ivs.Spe is { } v6) e.IV_SPE = v6;
    }
    if (body.EVs is { } evs)
    {
        if (evs.Hp is { } v) e.EV_HP = v;
        if (evs.Atk is { } v2) e.EV_ATK = v2;
        if (evs.Def is { } v3) e.EV_DEF = v3;
        if (evs.Spa is { } v4) e.EV_SPA = v4;
        if (evs.Spd is { } v5) e.EV_SPD = v5;
        if (evs.Spe is { } v6) e.EV_SPE = v6;
    }
    if (body.Moves is { } moves)
    {
        ushort[] slots = { 0, 0, 0, 0 };
        for (int i = 0; i < moves.Length && i < 4; i++)
            slots[i] = (ushort)moves[i];
        e.Move1 = slots[0]; e.Move2 = slots[1]; e.Move3 = slots[2]; e.Move4 = slots[3];
    }
}

// RequireIndex resolves a display name (exactly what /parse sent out) back to
// the raw id PKHeX's setters expect, the reverse of the bounds-checked lookup
// AddMon does going the other way.
static int RequireIndex(IReadOnlyList<string> table, string name, string field)
{
    for (int i = 0; i < table.Count; i++)
        if (table[i] == name)
            return i;
    throw new ArgumentException($"unknown {field} {name}");
}

app.Run();

record SpeciesEntry(int Dex, string Name);
record DexFlags(List<int> Seen, List<int> Caught);
record ParseResult(string Game, int Generation, TrainerDTO Trainer, int Count, int MaxSpecies, DexFlags Dex, List<ParsedMon> Pokemon, int BoxCount, int BoxSlotCount);
record Stats(int Hp, int Atk, int Def, int Spa, int Spd, int Spe);
record MoveDTO(int Id, string Name);
record ParsedMon(
    string Slot, int SpeciesId, string Species, string Nickname, int Level,
    string Nature, string Ability, bool Shiny, int Form, string Gender, bool IsEgg,
    int HeldItemId, string? HeldItem, int Ball, uint PID, string OT,
    // The snake_case policy mangles the acronyms IVs/EVs into "i_vs"/"e_vs"; pin
    // them to clean names so the Go client contract stays predictable.
    [property: JsonPropertyName("ivs")] Stats IVs,
    [property: JsonPropertyName("evs")] Stats EVs,
    MoveDTO[] Moves, string Blob);

// InjectRequest carries a stored Pokémon's raw bytes, the destination save's
// current bytes, and the exact box:slot the client picked from a rendered PC
// grid, no "first empty slot" search, since manual slot picking is required
// for PokeHome v1 (see the design doc's box UI decision). Generation
// disambiguates the entity bytes for DecodeEntity, the same as EditRequest.
// Overwrite lets the caller proceed past an occupied slot once it has already
// preserved the occupant elsewhere (see the occupied-slot check above).
record InjectRequest(string Entity, int Generation, string Save, int Box, int Slot, bool Overwrite = false);
record InjectResult(string Save, string Slot);

// EditRequest's fields are all optional: a request changes only what it sets,
// nature/ability/held_item taking the same display-name strings /parse
// already hands the Go side (see ApplyEdits) so there is no second numeric-id
// contract for callers to keep in sync with the read side.
record EditRequest(
    string Entity, int Generation,
    string? Nickname = null, int? Level = null, string? Nature = null, string? Ability = null,
    string? Gender = null, bool? Shiny = null, int? Form = null, string? HeldItem = null,
    int? Ball = null, string? OriginalTrainerName = null,
    // Same SnakeCaseLower-mangles-acronyms gotcha as ParsedMon.IVs/EVs above.
    [property: JsonPropertyName("ivs")] StatEdit? IVs = null,
    [property: JsonPropertyName("evs")] StatEdit? EVs = null,
    int[]? Moves = null);
record StatEdit(int? Hp = null, int? Atk = null, int? Def = null, int? Spa = null, int? Spd = null, int? Spe = null);

// BagItem/BagPouch (the actual pouch/item shape) live in Bag.cs, alongside
// the code that reads and writes them, these four are just the HTTP
// envelope around a Bag.Read/Bag.Write call.
record BagReadRequest(string Save);
record BagReadResult(BagPouch[] Pouches);
record BagWriteRequest(string Save, BagPouch[] Pouches);
record BagWriteResult(string Save);

// NamedEntry is the shape /abilities, /items, and /moves each return: a
// PKHeX table index paired with its display name, the same pairing
// SpeciesEntry already is for /species, just not species-specific.
record NamedEntry(int Id, string Name);

// CreateRequest builds a Pokémon that never came from a save. Species and
// Level are the only things a blank PKM cannot default sensibly on its own;
// every other field takes the same optional, name-string shape EditRequest
// does and is applied by the same ApplyEdits code.
record CreateRequest(
    int Generation, int Species, int Level = 5,
    string? Nickname = null, string? Nature = null, string? Ability = null,
    string? Gender = null, bool? Shiny = null, int? Form = null, string? HeldItem = null,
    int? Ball = null, string? OriginalTrainerName = null,
    [property: JsonPropertyName("ivs")] StatEdit? IVs = null,
    [property: JsonPropertyName("evs")] StatEdit? EVs = null,
    int[]? Moves = null);
