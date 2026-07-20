using PKHeX.Core;

// Reads and writes a save's item bag. A separate file/class rather than
// Program.cs local functions, so pkhex.tests can exercise it directly
// against a real save instead of only through an HTTP round-trip (see
// Legality.cs for the same reasoning).
//
// This walks SaveFile.Inventory -> Pouches -> Items via `dynamic`. Every
// generation has its own concrete pouch/item classes (InventoryPouch3,
// InventoryPouch4, InventoryPouch8b, InventoryPouchGB, ...) with no single
// shared base class or interface exposing Type/MaxCount/Items/Index/Count
// identically, confirmed empirically against the real save corpus (a
// reflection probe hit AmbiguousMatchException on SaveFile.Inventory itself,
// since every format overrides it covariantly), so static typing here would
// need a per-generation cast ladder for what is otherwise the same handful
// of fields everywhere. `dynamic` resolves each member at the concrete
// runtime type instead.
public static class Bag
{
    public static BagPouch[] Read(SaveFile sav)
    {
        var strings = GameInfo.GetStrings("en");
        dynamic bag = sav.Inventory;
        var result = new List<BagPouch>();
        foreach (var pouch in bag.Pouches)
        {
            var items = new List<BagItem>();
            int capacity = 0;
            foreach (var it in pouch.Items)
            {
                capacity++;
                int index = (int)it.Index;
                int count = (int)it.Count;
                if (index == 0 && count == 0)
                    continue; // an empty slot, only occupied ones are worth showing
                string? name = index > 0 && index < strings.Item.Count ? strings.Item[index] : null;
                items.Add(new BagItem(index, name, count));
            }
            string type = ((object)pouch.Type).ToString()!;
            result.Add(new BagPouch(type, (int)pouch.MaxCount, capacity, items.ToArray()));
        }
        return result.ToArray();
    }

    // Write applies pouches (by Type name) onto sav's own live bag objects.
    // A pouch this save doesn't have, or that the request doesn't mention,
    // is left untouched. Throws ArgumentException on anything that would
    // write invalid data (unknown item index, an out-of-range count, or more
    // items than the pouch has slots for), so a caller's mistake fails loudly
    // rather than silently corrupting the save.
    public static void Write(SaveFile sav, BagPouch[] pouches)
    {
        var strings = GameInfo.GetStrings("en");
        dynamic bag = sav.Inventory;
        foreach (var pouch in bag.Pouches)
        {
            string type = ((object)pouch.Type).ToString()!;
            var request = Array.Find(pouches, p => p.Type == type);
            if (request is null)
                continue; // this pouch was not part of the request, leave it untouched

            var arr = pouch.Items;
            int capacity = arr.Length;
            if (request.Items.Length > capacity)
                throw new ArgumentException($"{type} holds at most {capacity} item slots, got {request.Items.Length}");
            int maxCount = (int)pouch.MaxCount;
            for (int i = 0; i < capacity; i++)
            {
                if (i < request.Items.Length)
                {
                    var item = request.Items[i];
                    if (item.Index != 0 && (item.Index < 0 || item.Index >= strings.Item.Count))
                        throw new ArgumentException($"unknown item index {item.Index}");
                    if (item.Count < 0 || item.Count > maxCount)
                        throw new ArgumentException($"item count {item.Count} is out of range for {type} (max {maxCount})");
                    arr[i].Index = item.Index;
                    arr[i].Count = item.Count;
                }
                else
                {
                    arr[i].Index = 0;
                    arr[i].Count = 0;
                }
            }
        }
        // Mutating the pouch/item objects above only changes PKHeX's in-memory
        // model; CopyTo is the call that actually re-serializes the bag into
        // the save's own byte buffer (confirmed empirically, sav.Write()
        // without it silently discards every change made here).
        bag.CopyTo(sav);
    }
}

// BagItem is one occupied item slot: an index (a name /items already knows,
// resolved here for convenience so the client need not look it up itself)
// and how many are held.
public record BagItem(int Index, string? Name, int Count);

// BagPouch is one pouch of a save's bag, Items, KeyItems, Balls, TMHMs,
// Medicine, ... (PKHeX's own InventoryType enum names, used verbatim as the
// wire identifier so a write request can name exactly the pouch it means).
// MaxCount is the largest quantity a single item stack may hold in this
// pouch (999 for most modern generations, 99 for older ones); Capacity is
// how many item slots the pouch itself has room for. Items omits empty
// slots on read, Write pads whatever a write request doesn't supply back
// out to Capacity with empty slots, so a request need only describe what
// should be there now, not what to leave blank.
public record BagPouch(string Type, int MaxCount, int Capacity, BagItem[] Items);
