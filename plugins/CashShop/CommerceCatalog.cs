using System;
using System.Collections.Generic;
using Newtonsoft.Json;

namespace Bd2CashShop;

internal sealed class CommerceCatalog
{
    [JsonProperty("schema_version", Required = Required.Always)] public int SchemaVersion { get; set; }
    [JsonProperty("game_version", Required = Required.Always)] public string GameVersion { get; set; }
    [JsonProperty("products", Required = Required.Always)] public CommerceProduct[] Products { get; set; }

    internal Dictionary<string, CommerceProduct> Validate(string gameVersion)
    {
        if (SchemaVersion != 1 || GameVersion != gameVersion || Products == null)
            throw new InvalidOperationException("Commerce catalog version mismatch");
        var result = new Dictionary<string, CommerceProduct>(StringComparer.Ordinal);
        foreach (CommerceProduct product in Products)
        {
            if (product == null || string.IsNullOrWhiteSpace(product.Sku) || product.GroupId <= 0 || product.ProductId <= 0 ||
                product.SaleGroup < 0 || product.Amount < 0 || product.ItemType != 0 && product.ItemType != 2 && product.ItemType != 3 && product.ItemType != 4 ||
                product.ItemType == 0 && product.Amount != 0 || !product.Recharge && product.ItemType != 2 || result.ContainsKey(product.Key))
                throw new InvalidOperationException("Invalid or duplicate commerce SKU");
            result.Add(product.Key, product);
        }
        return result;
    }
}

internal sealed class CommerceProduct
{
    internal string Key => MakeKey(GroupId, ProductId, SaleGroup, Sku);
    internal static string MakeKey(int group, int id, int sale, string sku) => group + "/" + id + "/" + sale + "/" + sku;
    [JsonProperty("sku", Required = Required.Always)] public string Sku { get; set; }
    [JsonProperty("group_id", Required = Required.Always)] public int GroupId { get; set; }
    [JsonProperty("product_id", Required = Required.Always)] public int ProductId { get; set; }
    [JsonProperty("sale_group", Required = Required.Always)] public int SaleGroup { get; set; }
    [JsonProperty("item_type", Required = Required.Always)] public int ItemType { get; set; }
    [JsonProperty("amount", Required = Required.Always)] public long Amount { get; set; }
    [JsonProperty("recharge", Required = Required.Always)] public bool Recharge { get; set; }
    [JsonProperty("enabled", Required = Required.Always)] public bool Enabled { get; set; }
}
