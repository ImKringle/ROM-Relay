using System.Text.Json.Serialization;
using NDecrypt.Core;
using SabreTools.Security.Cryptography;

// ROM Relay NDecrypt sidecar: a localhost-only service the Go backend calls to
// encrypt/decrypt raw Nintendo DS/3DS cart dumps (.nds/.srl/.dsi/.ids,
// .3ds/.cci). NDecrypt.Core is .NET-only and has no NuGet package, so it is
// built from the third_party/ndecrypt submodule and lives here rather than in
// the Go application, the same shape the PKHeX sidecar already uses for
// PKHeX.Core.
//
// Scope: this wraps NDecrypt.Core exactly as-is. It has no seed-crypto (9.x
// "Seed" encryption) support at all - that would need code this sidecar does
// not add on top of the upstream library. It also has nothing to do with
// .cia files: NDecrypt.Core only has DSProcessor and ThreeDSProcessor, no CIA
// processor. .cia decrypt stays on the separate, existing Redux driver path.
//
// Endpoints:
//   GET  /health         -> readiness probe + whether keys were loaded
//   POST /info?ext=.3ds   raw cart bytes in, per-partition encryption status
//   POST /decrypt?ext=.3ds[&force=true]  raw cart bytes in, decrypted bytes out
//   POST /encrypt?ext=.3ds[&force=true]  raw cart bytes in, encrypted bytes out

var builder = WebApplication.CreateBuilder(args);
builder.Services.ConfigureHttpJsonOptions(o =>
{
    o.SerializerOptions.PropertyNamingPolicy = System.Text.Json.JsonNamingPolicy.SnakeCaseLower;
});
// Cart dumps run into multiple GB; Kestrel's 30 MB default would reject every
// real upload. The Go caller already enforces the real ceiling (MaxROMBytes)
// before it ever proxies a request here, on the same internal-network-only
// trust boundary the PKHeX sidecar uses.
builder.WebHost.ConfigureKestrel(o => o.Limits.MaxRequestBodySize = null);
var app = builder.Build();

// Keys are loaded once at startup, the same lazy-optional shape
// internal/converter.New(boot9Path) uses on the Go side: missing/invalid keys
// do not stop the sidecar from starting, they just mean Original-NCCH-keyed
// operations will fail per-request (NDecrypt.Core's own processors already
// answer false rather than throwing when key material is absent).
string configPath = Environment.GetEnvironmentVariable("NDECRYPT_CONFIG") ?? "/keys/config.json";
bool development = string.Equals(Environment.GetEnvironmentVariable("NDECRYPT_DEVELOPMENT"), "true", StringComparison.OrdinalIgnoreCase);
var (dsProcessor, threeDSProcessor, keysLoaded) = LoadProcessors(configPath, development);

app.MapGet("/health", () => Results.Json(new { status = "ok", library = "NDecrypt.Core", keys_loaded = keysLoaded }));

app.MapPost("/info", async (HttpRequest request) => await WithStagedFile(request, async path =>
{
    var proc = ProcessorFor(path, dsProcessor, threeDSProcessor);
    if (proc is null)
        return Results.BadRequest(new { error = "unrecognized cart extension" });
    string? info = proc.GetInformation(path);
    if (info is null)
        return Results.UnprocessableEntity(new { error = "could not read cart information" });
    return Results.Json(new { info });
}));

app.MapPost("/decrypt", async (HttpRequest request) => await RunCrypt(request, dsProcessor, threeDSProcessor, decrypt: true));
app.MapPost("/encrypt", async (HttpRequest request) => await RunCrypt(request, dsProcessor, threeDSProcessor, decrypt: false));

app.Run();

// RunCrypt stages the uploaded cart into a private temp file, runs the
// requested direction into a second temp file (never in place - the input the
// Go side staged from an upload or catalog artifact is never touched), and
// streams the result back. Both temp files are removed unconditionally.
static async Task<IResult> RunCrypt(HttpRequest request, ICartProcessor ds, ICartProcessor threeDS, bool decrypt)
{
    return await WithStagedFile(request, path =>
    {
        var proc = ProcessorFor(path, ds, threeDS);
        if (proc is null)
            return Task.FromResult<IResult>(Results.BadRequest(new { error = "unrecognized cart extension" }));

        bool force = request.Query.TryGetValue("force", out var f) && string.Equals(f, "true", StringComparison.OrdinalIgnoreCase);
        string outputPath = path + ".out";
        try
        {
            bool ok = decrypt ? proc.DecryptFile(path, outputPath, force) : proc.EncryptFile(path, outputPath, force);
            if (!ok || !File.Exists(outputPath))
                return Task.FromResult<IResult>(Results.UnprocessableEntity(new { error = $"{(decrypt ? "decrypt" : "encrypt")} failed - wrong keys, already in that state, or an unsupported title (e.g. seed crypto)" }));
            byte[] result = File.ReadAllBytes(outputPath);
            return Task.FromResult<IResult>(Results.Bytes(result, "application/octet-stream"));
        }
        finally
        {
            if (File.Exists(outputPath))
                File.Delete(outputPath);
        }
    });
}

// WithStagedFile writes the request body to a private temp file named with
// the caller-supplied extension (NDecrypt.Core dispatches by filename
// extension, it has no header-sniffing of its own) and guarantees cleanup.
static async Task<IResult> WithStagedFile(HttpRequest request, Func<string, Task<IResult>> body)
{
    string ext = request.Query.TryGetValue("ext", out var e) ? e.ToString() : "";
    if (ext.Length == 0 || ext.Contains("..") || ext.Contains('/') || ext.Contains('\\'))
        return Results.BadRequest(new { error = "ext query parameter is required and must be a plain extension" });
    if (!ext.StartsWith('.'))
        ext = "." + ext;

    string dir = Path.Combine(Path.GetTempPath(), "ndecrypt-" + Guid.NewGuid().ToString("N"));
    Directory.CreateDirectory(dir);
    string path = Path.Combine(dir, "cart" + ext);
    try
    {
        using var fs = new FileStream(path, FileMode.Create, FileAccess.Write);
        await request.Body.CopyToAsync(fs);
        if (fs.Length == 0)
            return Results.BadRequest(new { error = "empty request body" });
        return await body(path);
    }
    finally
    {
        try { Directory.Delete(dir, recursive: true); } catch { /* best effort */ }
    }
}

static ICartProcessor? ProcessorFor(string path, ICartProcessor ds, ICartProcessor threeDS) => Path.GetExtension(path).ToLowerInvariant() switch
{
    ".nds" or ".srl" or ".dsi" or ".ids" => ds,
    ".3ds" or ".cci" => threeDS,
    _ => null,
};

// LoadProcessors mirrors NDecrypt's own BaseFeature.InitializeTools: read a
// config.json of hex-string keys (see NDecryptConfig below - the same shape
// NDecrypt's CLI Configuration class uses, reimplemented here since that
// class is internal to the CLI project) and build the two processors NDecrypt
// itself only ever has - one DS, one 3DS. Missing/invalid config yields
// empty-keyed processors: every operation needing a key simply reports
// failure per-request rather than the sidecar refusing to start.
static (ICartProcessor ds, ICartProcessor threeDS, bool keysLoaded) LoadProcessors(string configPath, bool development)
{
    NDecryptConfig? config = null;
    if (File.Exists(configPath))
    {
        try
        {
            config = System.Text.Json.JsonSerializer.Deserialize<NDecryptConfig>(File.ReadAllText(configPath));
        }
        catch
        {
            config = null;
        }
    }
    if (config is null)
        return (new DSProcessor(), new ThreeDSProcessor(new N3DSEncryptionSettings()), false);

    var ds = new DSProcessor { BlowfishTable = FromHex(config.NitroEncryptionData) };
    var settings = new N3DSEncryptionSettings
    {
        Development = development,
        AESHardwareConstant = FromHex(config.AESHardwareConstant),
        KeyX0x18 = FromHex(config.KeyX0x18),
        KeyX0x1B = FromHex(config.KeyX0x1B),
        KeyX0x25 = FromHex(config.KeyX0x25),
        KeyX0x2C = FromHex(config.KeyX0x2C),
        DevKeyX0x18 = FromHex(config.DevKeyX0x18),
        DevKeyX0x1B = FromHex(config.DevKeyX0x1B),
        DevKeyX0x25 = FromHex(config.DevKeyX0x25),
        DevKeyX0x2C = FromHex(config.DevKeyX0x2C),
    };
    return (ds, new ThreeDSProcessor(settings), true);
}

static byte[] FromHex(string? hex)
{
    if (string.IsNullOrEmpty(hex))
        return [];
    hex = hex.Trim();
    if (hex.Length % 2 != 0)
        return [];
    var bytes = new byte[hex.Length / 2];
    for (int i = 0; i < bytes.Length; i++)
    {
        if (!byte.TryParse(hex.AsSpan(i * 2, 2), System.Globalization.NumberStyles.HexNumber, null, out bytes[i]))
            return [];
    }
    return bytes;
}

// NDecryptConfig is the same field shape as NDecrypt's own (internal, so not
// referenceable) Configuration class - one JSON file, hex-string key material,
// matching config-default.json in the third_party/ndecrypt submodule.
record NDecryptConfig(
    string? NitroEncryptionData,
    string? AESHardwareConstant,
    string? KeyX0x18, string? DevKeyX0x18,
    string? KeyX0x1B, string? DevKeyX0x1B,
    string? KeyX0x25, string? DevKeyX0x25,
    string? KeyX0x2C, string? DevKeyX0x2C);
