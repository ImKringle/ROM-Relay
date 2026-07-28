import com.dabomstew.pkrandom.MiscTweak;
import com.dabomstew.pkrandom.RandomSource;
import com.dabomstew.pkrandom.Settings;
import com.dabomstew.pkrandom.cli.CliRandomizer;
import com.dabomstew.pkrandom.pokemon.ExpCurve;
import com.dabomstew.pkrandom.pokemon.GenRestrictions;
import com.dabomstew.pkrandom.romhandlers.*;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;
import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.Executors;

// ROM Relay randomizer sidecar: a localhost-only service the Go backend calls to
// run the real Universal Pokemon Randomizer ZX (vendored jar, see Dockerfile)
// against a ROM. The randomization logic itself is tens of thousands of lines of
// generation-specific Java; this shim only translates a flat JSON settings
// object into calls on the real Settings class (never hand-rolls its binary
// .rnqs format, which is versioned/migrated upstream by SettingsUpdater) and
// then calls the project's own CliRandomizer in-process.
//
// Endpoints:
//   GET  /health        -> readiness probe
//   POST /randomize     -> header X-Randomizer-Settings: <flat JSON settings>,
//                           body: raw source ROM bytes.
//                           200 -> application/octet-stream randomized ROM bytes
//                           4xx -> {"error": "..."}
//   POST /capabilities  -> header X-Randomizer-Extension, body: raw source ROM
//                           bytes. 200 -> {"generation": N, "misc_tweaks": [...]}
//                           listing which misc tweak keys this exact ROM supports.
public class Shim {

    public static void main(String[] args) throws IOException {
        int port = 8083;
        String envPort = System.getenv("RANDOMIZER_PORT");
        if (envPort != null && !envPort.isEmpty()) {
            port = Integer.parseInt(envPort);
        }
        HttpServer server = HttpServer.create(new InetSocketAddress("0.0.0.0", port), 0);
        server.createContext("/health", Shim::health);
        server.createContext("/randomize", Shim::randomize);
        server.createContext("/capabilities", Shim::capabilities);
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        System.out.println("randomizer sidecar listening on :" + port);
    }

    private static void health(HttpExchange ex) throws IOException {
        byte[] body = "{\"status\":\"ok\",\"library\":\"UniversalPokemonRandomizerZX\"}".getBytes(StandardCharsets.UTF_8);
        ex.getResponseHeaders().set("Content-Type", "application/json");
        ex.sendResponseHeaders(200, body.length);
        try (var out = ex.getResponseBody()) {
            out.write(body);
        }
    }

    private static final java.util.Set<String> ACCEPTED_EXTENSIONS = java.util.Set.of(".gb", ".sgb", ".gbc", ".gba", ".nds", ".ds");

    private static void randomize(HttpExchange ex) {
        File work = null;
        try {
            if (!"POST".equals(ex.getRequestMethod())) {
                sendError(ex, 405, "method not allowed");
                return;
            }
            String settingsHeader = ex.getRequestHeaders().getFirst("X-Randomizer-Settings");
            String extension = ex.getRequestHeaders().getFirst("X-Randomizer-Extension");
            if (settingsHeader == null || settingsHeader.isEmpty()) {
                sendError(ex, 400, "X-Randomizer-Settings header is required");
                return;
            }
            if (extension == null || !ACCEPTED_EXTENSIONS.contains(extension.toLowerCase())) {
                sendError(ex, 400, "unsupported or missing X-Randomizer-Extension (expected one of " + ACCEPTED_EXTENSIONS + ")");
                return;
            }
            JSONObject json;
            try {
                json = new JSONObject(settingsHeader);
            } catch (Exception e) {
                sendError(ex, 400, "invalid settings JSON: " + e.getMessage());
                return;
            }

            byte[] rom = readAll(ex.getRequestBody());
            if (rom.length == 0) {
                sendError(ex, 400, "empty ROM body");
                return;
            }

            work = Files.createTempDirectory("randomize-").toFile();
            File inFile = new File(work, "in" + extension);
            File outFile = new File(work, "out" + extension);
            File settingsFile = new File(work, "settings.rnqs");
            Files.write(inFile.toPath(), rom);

            Settings settings;
            try {
                settings = buildSettings(json);
            } catch (Exception e) {
                sendError(ex, 400, "invalid settings: " + e.getMessage());
                return;
            }
            try (var fos = new java.io.FileOutputStream(settingsFile)) {
                settings.write(fos);
            }

            int code = CliRandomizer.invoke(new String[]{
                    "-s", settingsFile.getAbsolutePath(),
                    "-i", inFile.getAbsolutePath(),
                    "-o", outFile.getAbsolutePath(),
            });
            if (code != 0 || !outFile.exists()) {
                sendError(ex, 422, "randomization failed (see sidecar logs)");
                return;
            }

            byte[] out = Files.readAllBytes(outFile.toPath());
            ex.getResponseHeaders().set("Content-Type", "application/octet-stream");
            ex.sendResponseHeaders(200, out.length);
            try (var os = ex.getResponseBody()) {
                os.write(out);
            }
        } catch (Exception e) {
            try {
                sendError(ex, 500, "internal error: " + e);
            } catch (IOException ignored) {
            }
        } finally {
            if (work != null) {
                deleteRecursive(work);
            }
        }
    }

    // capabilities loads a ROM just far enough to identify which RomHandler
    // (i.e. which generation) accepts it, and reports which misc tweaks that
    // handler actually supports (RomHandler.miscTweaksAvailable() varies not
    // just by generation but by specific game/revision). This is the same
    // detection loop CliRandomizer.performDirectRandomization uses before
    // randomizing; run here standalone so the UI can hide options that would
    // otherwise be silently dropped by Settings.tweakForRom at randomize time.
    private static void capabilities(HttpExchange ex) {
        File work = null;
        try {
            if (!"POST".equals(ex.getRequestMethod())) {
                sendError(ex, 405, "method not allowed");
                return;
            }
            String extension = ex.getRequestHeaders().getFirst("X-Randomizer-Extension");
            if (extension == null || !ACCEPTED_EXTENSIONS.contains(extension.toLowerCase())) {
                sendError(ex, 400, "unsupported or missing X-Randomizer-Extension (expected one of " + ACCEPTED_EXTENSIONS + ")");
                return;
            }
            byte[] rom = readAll(ex.getRequestBody());
            if (rom.length == 0) {
                sendError(ex, 400, "empty ROM body");
                return;
            }

            work = Files.createTempDirectory("caps-").toFile();
            File inFile = new File(work, "in" + extension);
            Files.write(inFile.toPath(), rom);

            RomHandler.Factory[] checkHandlers = new RomHandler.Factory[]{
                    new Gen1RomHandler.Factory(),
                    new Gen2RomHandler.Factory(),
                    new Gen3RomHandler.Factory(),
                    new Gen4RomHandler.Factory(),
                    new Gen5RomHandler.Factory(),
            };
            for (RomHandler.Factory rhf : checkHandlers) {
                if (!rhf.isLoadable(inFile.getAbsolutePath())) {
                    continue;
                }
                RomHandler romHandler = rhf.create(RandomSource.instance());
                romHandler.loadRom(inFile.getAbsolutePath());
                int available = romHandler.miscTweaksAvailable();
                JSONArray tweaks = new JSONArray();
                for (Map.Entry<String, MiscTweak> entry : MISC_TWEAK_NAMES.entrySet()) {
                    if ((available & entry.getValue().getValue()) != 0) {
                        tweaks.put(entry.getKey());
                    }
                }
                JSONObject resp = new JSONObject();
                resp.put("generation", romHandler.generationOfPokemon());
                resp.put("misc_tweaks", tweaks);
                byte[] body = resp.toString().getBytes(StandardCharsets.UTF_8);
                ex.getResponseHeaders().set("Content-Type", "application/json");
                ex.sendResponseHeaders(200, body.length);
                try (var out = ex.getResponseBody()) {
                    out.write(body);
                }
                return;
            }
            sendError(ex, 422, "could not detect a supported ROM handler for this file");
        } catch (Exception e) {
            try {
                sendError(ex, 500, "internal error: " + e);
            } catch (IOException ignored) {
            }
        } finally {
            if (work != null) {
                deleteRecursive(work);
            }
        }
    }

    // buildSettings translates the flat client JSON into a real Settings object
    // via its own public setters, one field/category at a time, following
    // Settings.java's own grouping. Gen 6/7-only fields (totem/ally/aura
    // Pokemon, the 3DS-only mods) are intentionally not wired here: ROM Relay's
    // randomizer tool is Gen 1-5 cartridge only in this version, and those
    // fields are no-ops on those generations regardless (Settings.tweakForRom
    // strips anything the loaded ROM doesn't support).
    private static Settings buildSettings(JSONObject j) {
        Settings s = new Settings();
        // Settings.write() serializes romName unconditionally too; it's purely
        // informational (the GUI's "last ROM used" label), never null.
        s.setRomName("");

        // General / Pokemon traits
        s.setLimitPokemon(bool(j, "limit_pokemon", false));
        if (s.isLimitPokemon()) {
            GenRestrictions gr = new GenRestrictions();
            gr.allow_gen1 = bool(j, "allow_gen1", true);
            gr.allow_gen2 = bool(j, "allow_gen2", true);
            gr.allow_gen3 = bool(j, "allow_gen3", true);
            gr.allow_gen4 = bool(j, "allow_gen4", true);
            gr.allow_gen5 = bool(j, "allow_gen5", true);
            gr.allow_evolutionary_relatives = bool(j, "allow_evolutionary_relatives", false);
            s.setCurrentRestrictions(gr);
        }
        s.setRaceMode(bool(j, "race_mode", false));
        s.setBlockBrokenMoves(bool(j, "block_broken_moves", true));
        s.setBanIrregularAltFormes(bool(j, "ban_irregular_alt_formes", false));
        s.setDualTypeOnly(bool(j, "dual_type_only", false));
        s.setCurrentMiscTweaks(miscTweaks(j));

        // Base statistics
        s.setBaseStatisticsMod(oneHot(j, "base_statistics_mod", "unchanged", "shuffle", "random"));
        s.setBaseStatsFollowEvolutions(bool(j, "base_stats_follow_evolutions", false));
        s.setBaseStatsFollowMegaEvolutions(bool(j, "base_stats_follow_mega_evolutions", false));
        s.setAssignEvoStatsRandomly(bool(j, "assign_evo_stats_randomly", false));
        s.setUpdateBaseStats(bool(j, "update_base_stats", false));
        if (s.isUpdateBaseStats()) {
            s.setUpdateBaseStatsToGeneration(intv(j, "update_base_stats_to_generation", 5));
        }
        s.setStandardizeEXPCurves(bool(j, "standardize_exp_curves", false));
        if (s.isStandardizeEXPCurves()) {
            s.setExpCurveMod(oneHot(j, "exp_curve_mod", "legendaries", "strong_legendaries", "all"));
        }
        // Settings.write() serializes selectedEXPCurve unconditionally (it's
        // read back by Settings.read() the same way regardless of whether
        // standardizeEXPCurves is set), so this must never be left null even
        // when the client doesn't ask to standardize EXP curves.
        s.setSelectedEXPCurve(expCurve(j.optString("selected_exp_curve", "medium_fast")));

        // Types
        s.setTypesMod(oneHot(j, "types_mod", "unchanged", "random_follow_evolutions", "completely_random"));
        s.setTypesFollowMegaEvolutions(bool(j, "types_follow_mega_evolutions", false));

        // Abilities
        s.setAbilitiesMod(oneHot(j, "abilities_mod", "unchanged", "randomize"));
        s.setAllowWonderGuard(bool(j, "allow_wonder_guard", true));
        s.setAbilitiesFollowEvolutions(bool(j, "abilities_follow_evolutions", false));
        s.setAbilitiesFollowMegaEvolutions(bool(j, "abilities_follow_mega_evolutions", false));
        s.setBanTrappingAbilities(bool(j, "ban_trapping_abilities", false));
        s.setBanNegativeAbilities(bool(j, "ban_negative_abilities", false));
        s.setBanBadAbilities(bool(j, "ban_bad_abilities", false));
        s.setWeighDuplicateAbilitiesTogether(bool(j, "weigh_duplicate_abilities_together", true));
        s.setEnsureTwoAbilities(bool(j, "ensure_two_abilities", true));

        // Starters
        s.setStartersMod(oneHot(j, "starters_mod", "unchanged", "custom", "completely_random", "random_with_two_evolutions"));
        if (s.getStartersMod() == Settings.StartersMod.CUSTOM) {
            JSONArray custom = j.optJSONArray("custom_starters");
            int[] picks = new int[3];
            if (custom != null) {
                for (int i = 0; i < 3 && i < custom.length(); i++) {
                    picks[i] = custom.optInt(i, 1);
                }
            }
            s.setCustomStarters(picks);
        }
        s.setRandomizeStartersHeldItems(bool(j, "randomize_starters_held_items", false));
        s.setBanBadRandomStarterHeldItems(bool(j, "ban_bad_random_starter_held_items", false));
        s.setAllowStarterAltFormes(bool(j, "allow_starter_alt_formes", false));
        s.setLimitMainGameLegendaries(bool(j, "limit_main_game_legendaries", false));
        s.setLimit600(bool(j, "limit600", false));

        // Evolutions
        s.setEvolutionsMod(oneHot(j, "evolutions_mod", "unchanged", "random", "random_every_level"));
        s.setEvosSimilarStrength(bool(j, "evos_similar_strength", false));
        s.setEvosSameTyping(bool(j, "evos_same_typing", false));
        s.setEvosMaxThreeStages(bool(j, "evos_max_three_stages", false));
        s.setEvosForceChange(bool(j, "evos_force_change", false));
        s.setEvosAllowAltFormes(bool(j, "evos_allow_alt_formes", false));
        s.setChangeImpossibleEvolutions(bool(j, "change_impossible_evolutions", false));
        s.setMakeEvolutionsEasier(bool(j, "make_evolutions_easier", false));
        s.setRemoveTimeBasedEvolutions(bool(j, "remove_time_based_evolutions", false));

        // Move data
        s.setRandomizeMovePowers(bool(j, "randomize_move_powers", false));
        s.setRandomizeMoveAccuracies(bool(j, "randomize_move_accuracies", false));
        s.setRandomizeMovePPs(bool(j, "randomize_move_pps", false));
        s.setRandomizeMoveTypes(bool(j, "randomize_move_types", false));
        s.setRandomizeMoveCategory(bool(j, "randomize_move_category", false));
        s.setUpdateMoves(bool(j, "update_moves", false));
        if (s.isUpdateMoves()) {
            s.setUpdateMovesToGeneration(intv(j, "update_moves_to_generation", 5));
        }

        // Movesets
        s.setMovesetsMod(oneHot(j, "movesets_mod", "unchanged", "random_prefer_same_type", "completely_random", "metronome_only"));
        s.setStartWithGuaranteedMoves(bool(j, "start_with_guaranteed_moves", false));
        if (s.isStartWithGuaranteedMoves()) {
            s.setGuaranteedMoveCount(intv(j, "guaranteed_move_count", 2));
        }
        s.setReorderDamagingMoves(bool(j, "reorder_damaging_moves", true));
        s.setMovesetsForceGoodDamaging(bool(j, "movesets_force_good_damaging", false));
        if (s.isMovesetsForceGoodDamaging()) {
            s.setMovesetsGoodDamagingPercent(intv(j, "movesets_good_damaging_percent", 0));
        }
        s.setBlockBrokenMovesetMoves(bool(j, "block_broken_moveset_moves", true));
        s.setEvolutionMovesForAll(bool(j, "evolution_moves_for_all", false));

        // Trainers
        s.setTrainersMod(oneHot(j, "trainers_mod", "unchanged", "random", "distributed", "mainplaythrough", "type_themed", "type_themed_elite4_gyms"));
        s.setRivalCarriesStarterThroughout(bool(j, "rival_carries_starter_throughout", false));
        s.setTrainersUsePokemonOfSimilarStrength(bool(j, "trainers_use_pokemon_of_similar_strength", false));
        s.setTrainersMatchTypingDistribution(bool(j, "trainers_match_typing_distribution", false));
        s.setTrainersBlockLegendaries(bool(j, "trainers_block_legendaries", true));
        s.setTrainersBlockEarlyWonderGuard(bool(j, "trainers_block_early_wonder_guard", true));
        s.setRandomizeTrainerNames(bool(j, "randomize_trainer_names", false));
        s.setRandomizeTrainerClassNames(bool(j, "randomize_trainer_class_names", false));
        s.setTrainersForceFullyEvolved(bool(j, "trainers_force_fully_evolved", false));
        if (s.isTrainersForceFullyEvolved()) {
            s.setTrainersForceFullyEvolvedLevel(intv(j, "trainers_force_fully_evolved_level", 30));
        }
        s.setTrainersLevelModified(bool(j, "trainers_level_modified", false));
        if (s.isTrainersLevelModified()) {
            s.setTrainersLevelModifier(intv(j, "trainers_level_modifier", 0));
        }
        s.setEliteFourUniquePokemonNumber(intv(j, "elite_four_unique_pokemon_number", 0));
        s.setAllowTrainerAlternateFormes(bool(j, "allow_trainer_alternate_formes", false));
        s.setSwapTrainerMegaEvos(bool(j, "swap_trainer_mega_evos", false));
        s.setAdditionalBossTrainerPokemon(intv(j, "additional_boss_trainer_pokemon", 0));
        s.setAdditionalImportantTrainerPokemon(intv(j, "additional_important_trainer_pokemon", 0));
        s.setAdditionalRegularTrainerPokemon(intv(j, "additional_regular_trainer_pokemon", 0));
        s.setRandomizeHeldItemsForBossTrainerPokemon(bool(j, "randomize_held_items_for_boss_trainer_pokemon", false));
        s.setRandomizeHeldItemsForImportantTrainerPokemon(bool(j, "randomize_held_items_for_important_trainer_pokemon", false));
        s.setRandomizeHeldItemsForRegularTrainerPokemon(bool(j, "randomize_held_items_for_regular_trainer_pokemon", false));
        s.setConsumableItemsOnlyForTrainers(bool(j, "consumable_items_only_for_trainers", true));
        s.setSensibleItemsOnlyForTrainers(bool(j, "sensible_items_only_for_trainers", true));
        s.setHighestLevelGetsItemsForTrainers(bool(j, "highest_level_gets_items_for_trainers", false));
        s.setDoubleBattleMode(bool(j, "double_battle_mode", false));
        s.setShinyChance(bool(j, "shiny_chance", false));
        s.setBetterTrainerMovesets(bool(j, "better_trainer_movesets", false));

        // Wild Pokemon
        s.setWildPokemonMod(oneHot(j, "wild_pokemon_mod", "unchanged", "random", "area_mapping", "global_mapping"));
        s.setWildPokemonRestrictionMod(oneHot(j, "wild_pokemon_restriction_mod", "none", "similar_strength", "catch_em_all", "type_theme_areas"));
        s.setUseTimeBasedEncounters(bool(j, "use_time_based_encounters", false));
        s.setBlockWildLegendaries(bool(j, "block_wild_legendaries", true));
        s.setUseMinimumCatchRate(bool(j, "use_minimum_catch_rate", false));
        if (s.isUseMinimumCatchRate()) {
            s.setMinimumCatchRateLevel(intv(j, "minimum_catch_rate_level", 1));
        }
        s.setRandomizeWildPokemonHeldItems(bool(j, "randomize_wild_pokemon_held_items", false));
        s.setBanBadRandomWildPokemonHeldItems(bool(j, "ban_bad_random_wild_pokemon_held_items", false));
        s.setBalanceShakingGrass(bool(j, "balance_shaking_grass", false));
        s.setWildLevelsModified(bool(j, "wild_levels_modified", false));
        if (s.isWildLevelsModified()) {
            s.setWildLevelModifier(intv(j, "wild_level_modifier", 0));
        }
        s.setAllowWildAltFormes(bool(j, "allow_wild_alt_formes", false));

        // Static Pokemon
        s.setStaticPokemonMod(oneHot(j, "static_pokemon_mod", "unchanged", "random_matching", "completely_random", "similar_strength"));
        s.setAllowStaticAltFormes(bool(j, "allow_static_alt_formes", false));
        s.setSwapStaticMegaEvos(bool(j, "swap_static_mega_evos", false));
        s.setStaticLevelModified(bool(j, "static_level_modified", false));
        if (s.isStaticLevelModified()) {
            s.setStaticLevelModifier(intv(j, "static_level_modifier", 0));
        }
        s.setCorrectStaticMusic(bool(j, "correct_static_music", true));

        // TMs/HMs
        s.setTmsMod(oneHot(j, "tms_mod", "unchanged", "random"));
        s.setTmLevelUpMoveSanity(bool(j, "tm_level_up_move_sanity", true));
        s.setKeepFieldMoveTMs(bool(j, "keep_field_move_tms", true));
        s.setFullHMCompat(bool(j, "full_hm_compat", false));
        s.setTmsForceGoodDamaging(bool(j, "tms_force_good_damaging", false));
        if (s.isTmsForceGoodDamaging()) {
            s.setTmsGoodDamagingPercent(intv(j, "tms_good_damaging_percent", 0));
        }
        s.setBlockBrokenTMMoves(bool(j, "block_broken_tm_moves", true));
        s.setTmsFollowEvolutions(bool(j, "tms_follow_evolutions", false));
        s.setTmsHmsCompatibilityMod(oneHot(j, "tms_hms_compatibility_mod", "unchanged", "random_prefer_type", "completely_random", "full"));

        // Move tutors
        s.setMoveTutorMovesMod(oneHot(j, "move_tutor_moves_mod", "unchanged", "random"));
        s.setTutorLevelUpMoveSanity(bool(j, "tutor_level_up_move_sanity", true));
        s.setKeepFieldMoveTutors(bool(j, "keep_field_move_tutors", true));
        s.setTutorsForceGoodDamaging(bool(j, "tutors_force_good_damaging", false));
        if (s.isTutorsForceGoodDamaging()) {
            s.setTutorsGoodDamagingPercent(intv(j, "tutors_good_damaging_percent", 0));
        }
        s.setBlockBrokenTutorMoves(bool(j, "block_broken_tutor_moves", true));
        s.setTutorFollowEvolutions(bool(j, "tutor_follow_evolutions", false));
        s.setMoveTutorsCompatibilityMod(oneHot(j, "move_tutors_compatibility_mod", "unchanged", "random_prefer_type", "completely_random", "full"));

        // In-game trades
        s.setInGameTradesMod(oneHot(j, "in_game_trades_mod", "unchanged", "randomize_given", "randomize_given_and_requested"));
        s.setRandomizeInGameTradesNicknames(bool(j, "randomize_in_game_trades_nicknames", false));
        s.setRandomizeInGameTradesOTs(bool(j, "randomize_in_game_trades_ots", false));
        s.setRandomizeInGameTradesIVs(bool(j, "randomize_in_game_trades_ivs", false));
        s.setRandomizeInGameTradesItems(bool(j, "randomize_in_game_trades_items", false));

        // Items
        s.setFieldItemsMod(oneHot(j, "field_items_mod", "unchanged", "shuffle", "random", "random_even"));
        s.setBanBadRandomFieldItems(bool(j, "ban_bad_random_field_items", true));
        s.setShopItemsMod(oneHot(j, "shop_items_mod", "unchanged", "shuffle", "random"));
        s.setBanBadRandomShopItems(bool(j, "ban_bad_random_shop_items", true));
        s.setBanRegularShopItems(bool(j, "ban_regular_shop_items", false));
        s.setBanOPShopItems(bool(j, "ban_op_shop_items", false));
        s.setBalanceShopPrices(bool(j, "balance_shop_prices", false));
        s.setGuaranteeEvolutionItems(bool(j, "guarantee_evolution_items", false));
        s.setGuaranteeXItems(bool(j, "guarantee_x_items", false));
        s.setPickupItemsMod(oneHot(j, "pickup_items_mod", "unchanged", "random"));
        s.setBanBadRandomPickupItems(bool(j, "ban_bad_random_pickup_items", true));

        return s;
    }

    // MISC_TWEAK_NAMES maps the client's flat boolean keys to the real
    // MiscTweak constants, so the bitmask is always built from the vendored
    // jar's own values, never a hand-copied bit position.
    private static final Map<String, MiscTweak> MISC_TWEAK_NAMES = new HashMap<>();

    static {
        MISC_TWEAK_NAMES.put("bw_exp_patch", MiscTweak.BW_EXP_PATCH);
        MISC_TWEAK_NAMES.put("nerf_x_accuracy", MiscTweak.NERF_X_ACCURACY);
        MISC_TWEAK_NAMES.put("fix_crit_rate", MiscTweak.FIX_CRIT_RATE);
        MISC_TWEAK_NAMES.put("fastest_text", MiscTweak.FASTEST_TEXT);
        MISC_TWEAK_NAMES.put("running_shoes_indoors", MiscTweak.RUNNING_SHOES_INDOORS);
        MISC_TWEAK_NAMES.put("randomize_pc_potion", MiscTweak.RANDOMIZE_PC_POTION);
        MISC_TWEAK_NAMES.put("allow_pikachu_evolution", MiscTweak.ALLOW_PIKACHU_EVOLUTION);
        MISC_TWEAK_NAMES.put("national_dex_at_start", MiscTweak.NATIONAL_DEX_AT_START);
        MISC_TWEAK_NAMES.put("update_type_effectiveness", MiscTweak.UPDATE_TYPE_EFFECTIVENESS);
        MISC_TWEAK_NAMES.put("force_challenge_mode", MiscTweak.FORCE_CHALLENGE_MODE);
        MISC_TWEAK_NAMES.put("lower_case_pokemon_names", MiscTweak.LOWER_CASE_POKEMON_NAMES);
        MISC_TWEAK_NAMES.put("randomize_catching_tutorial", MiscTweak.RANDOMIZE_CATCHING_TUTORIAL);
        MISC_TWEAK_NAMES.put("ban_lucky_egg", MiscTweak.BAN_LUCKY_EGG);
        MISC_TWEAK_NAMES.put("no_free_lucky_egg", MiscTweak.NO_FREE_LUCKY_EGG);
        MISC_TWEAK_NAMES.put("ban_big_maniac_items", MiscTweak.BAN_BIG_MANIAC_ITEMS);
        MISC_TWEAK_NAMES.put("sos_battles_for_all", MiscTweak.SOS_BATTLES_FOR_ALL);
        MISC_TWEAK_NAMES.put("balance_static_levels", MiscTweak.BALANCE_STATIC_LEVELS);
        MISC_TWEAK_NAMES.put("retain_alt_formes", MiscTweak.RETAIN_ALT_FORMES);
        MISC_TWEAK_NAMES.put("run_without_running_shoes", MiscTweak.RUN_WITHOUT_RUNNING_SHOES);
        MISC_TWEAK_NAMES.put("faster_hp_and_exp_bars", MiscTweak.FASTER_HP_AND_EXP_BARS);
        MISC_TWEAK_NAMES.put("fast_distortion_world", MiscTweak.FAST_DISTORTION_WORLD);
        MISC_TWEAK_NAMES.put("update_rotom_forme_typing", MiscTweak.UPDATE_ROTOM_FORME_TYPING);
        MISC_TWEAK_NAMES.put("disable_low_hp_music", MiscTweak.DISABLE_LOW_HP_MUSIC);
    }

    private static int miscTweaks(JSONObject j) {
        JSONArray names = j.optJSONArray("misc_tweaks");
        int bits = 0;
        if (names != null) {
            for (int i = 0; i < names.length(); i++) {
                MiscTweak t = MISC_TWEAK_NAMES.get(names.optString(i, "").toLowerCase());
                if (t != null) {
                    bits |= t.getValue();
                }
            }
        }
        return bits;
    }

    // oneHot resolves a string enum value from the client JSON to the boolean...
    // one-hot array Settings's set<X>Mod(boolean...) setters expect, in the
    // exact declaration order of the corresponding nested enum.
    private static boolean[] oneHot(JSONObject j, String key, String... values) {
        String want = j.optString(key, values[0]).toLowerCase();
        boolean[] out = new boolean[values.length];
        boolean matched = false;
        for (int i = 0; i < values.length; i++) {
            if (values[i].equals(want)) {
                out[i] = true;
                matched = true;
            }
        }
        if (!matched) {
            throw new IllegalArgumentException(key + ": unknown value \"" + want + "\" (expected one of " + String.join(",", values) + ")");
        }
        return out;
    }

    private static boolean bool(JSONObject j, String key, boolean def) {
        return j.optBoolean(key, def);
    }

    private static int intv(JSONObject j, String key, int def) {
        return j.optInt(key, def);
    }

    private static ExpCurve expCurve(String name) {
        try {
            return ExpCurve.valueOf(name.toUpperCase());
        } catch (IllegalArgumentException e) {
            return ExpCurve.MEDIUM_FAST;
        }
    }

    private static byte[] readAll(InputStream in) throws IOException {
        ByteArrayOutputStream buf = new ByteArrayOutputStream();
        byte[] chunk = new byte[1 << 20];
        int n;
        while ((n = in.read(chunk)) != -1) {
            buf.write(chunk, 0, n);
        }
        return buf.toByteArray();
    }

    private static void sendError(HttpExchange ex, int status, String message) throws IOException {
        byte[] body = new JSONObject().put("error", message).toString().getBytes(StandardCharsets.UTF_8);
        ex.getResponseHeaders().set("Content-Type", "application/json");
        ex.sendResponseHeaders(status, body.length);
        try (var out = ex.getResponseBody()) {
            out.write(body);
        }
    }

    private static void deleteRecursive(File f) {
        File[] children = f.listFiles();
        if (children != null) {
            for (File c : children) {
                deleteRecursive(c);
            }
        }
        f.delete();
    }
}
