using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Reflection;
using System.Threading.Tasks;
using BD2.GameNames;
using BepInEx;
using HarmonyLib;
using Newtonsoft.Json;
using TMPro;
using UnityEngine;
using gamfs.Platform;
using gamfs;
using BDNetwork;
using Proto.Net;
using Proto.Design.common;
using Google.Protobuf;

namespace Bd2CashShop;

[BepInPlugin("bd2.cashshop", "BD2 Cash Shop", Bd2Build.Versions.Plugin)]
[BepInDependency("bd2.localidentity")]
public sealed class Plugin : BaseUnityPlugin
{
    private static Harmony harmony;
    private static CommerceHost host;
    private static bool applicationQuitting;
    private static Dictionary<string, CommerceProduct> products;
    private static string catalogOrigin;
    private static readonly Dictionary<UnityEngine.Object, Action> priceViews = new Dictionary<UnityEngine.Object, Action>();
    private static readonly HashSet<string> attendanceReceipts = new HashSet<string>(StringComparer.Ordinal);
    private static readonly HashSet<long> attendanceRefreshPayments = new HashSet<long>();
    private sealed class PriceLabelState
    {
        internal string Text;
        internal bool Active;
        internal TextAlignmentOptions Alignment;
        internal bool Wrap;
        internal Vector2 AnchorMin, AnchorMax, Pivot, OffsetMin, OffsetMax;
        internal bool Layout;
        internal Dictionary<Behaviour, bool> Drivers;
    }
    private static readonly Dictionary<CurrencyButton, Dictionary<TMP_Text, PriceLabelState>> unavailablePriceLabels = new Dictionary<CurrencyButton, Dictionary<TMP_Text, PriceLabelState>>();
    private static BepInEx.Logging.ManualLogSource Log;
    private const string Unavailable = "当前服务器未开通购买功能";
    private const string Marker = "<link=bd2-commerce:";
    private static long paySequence = DateTime.UtcNow.Ticks;
    private static readonly BindingFlags All = BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Instance | BindingFlags.Static;

    private void Awake()
    {
        try
        {
            Log = Logger;
            Game.Validate(typeof(Plugin).Assembly, Bd2Build.Versions.Game, message => Logger.LogInfo(message));
            harmony = new Harmony("bd2.cashshop");
            Patch(typeof(PlatformManager), "Purchase", nameof(Purchase), false, 3);
            Patch(typeof(PlatformManager), "FinishPurchase", nameof(Suppress), false, 2);
            Patch(typeof(PlatformManager), "RetryUnfinishedPurchase", nameof(Suppress), false, 0);
            Patch(typeof(PlatformManager), "SendInAppEventPurchase", nameof(Suppress), false, 4);
            Patch(typeof(CashShopPacket), "SendInAppEventPurchase", nameof(Suppress), false, 2);
            Patch(typeof(CashShopPacket), "AddSubscribeAttendanceInfo", nameof(Suppress), false, 1);
            Patch(typeof(CashShopPacket), "SendPlatformCashShopBuyRequest", nameof(BeforeRequest), false, 10);
            Patch(typeof(PlatformManager), "GetProductMarketPriceWithSymbol", nameof(PriceLabel), false, 1);
            Patch(typeof(PlatformManager), "GetProductMarketPriceOnly", nameof(PriceNumber), false, 1);
            Patch(typeof(PlatformManager), "GetProductCurrencyCode", nameof(CurrencyCode), false, 1);
            Patch(typeof(PlatformRuler), "GetProductAsync", nameof(ProductPrefetch), false, 6);
            Patch(typeof(PlatformManager), "GetPriceLocalization", nameof(LocalPriceLocalization), false, 4);
            Patch(typeof(PlatformRuler), "InitializeGPG", nameof(SkipOfficialBilling), false, 1);
            Patch(typeof(PlatformRuler), "CheckGPGOAuthStatus", nameof(LocalBillingStatus), false, 1);
            Patch(typeof(PlatformRuler), "OnGetPriceLocalizationBySplit", nameof(LocalSplitPrices), false, 4);
            Patch(typeof(CurrencyButton), "SetCash", nameof(SetCash), false, 1);
            Patch(typeof(CashShopBuyPopupUI), "SetProductInfo", nameof(PopupPrice), true, 8);
            MethodInfo paymentPopup = typeof(UIManager).GetGameMethod("OpenSelectiveMessagePopupUI", All, null,
                new[] { typeof(Action<MessagePopupUI>), typeof(string), typeof(string), typeof(string),
                    typeof(Action<bool, MessagePopupUI>), typeof(bool), typeof(bool), typeof(MessagePopupUI.EMessagePopupButtonStyle) }, null);
            if (paymentPopup == null) throw new MissingMethodException("UIManager payment confirmation overload");
            harmony.Patch(paymentPopup, prefix: new HarmonyMethod(typeof(Plugin), nameof(SuppressExternalPaymentConfirmation)));
            Patch(typeof(PackagePurchaseButton), "SetPrice", nameof(PackagePrice), true, 0);
            Patch(typeof(PassRootUI), "CheckIsInAppBuyButton", nameof(PassPrice), true, 0);
            Patch(typeof(BuyConfirmPopupUI), "SetPassBuyPopup", nameof(PassPopupPrice), true, 5);
            Patch(typeof(GachaProductButtonElement), "Set", nameof(GachaPrice), true, 6);
            PatchGeneratedReceiver("<SendLoginPassRequest>b__0", nameof(LoginPassRewards));
            PatchGeneratedReceiver("<SendEventRewardRequest>b__0", nameof(EventRewards));
            PassRewardPresentation.Install(harmony);
            RewardBuffRefresh.Install(harmony);
            PrestigeSkinRewardPresentation.Install(harmony, Log);
            MethodInfo attendance = typeof(EventPacket).GetGameMethod("RecvAttendanceResponse", All, null,
                new[] { typeof(IMessage), typeof(int), typeof(int) }, null);
            if (attendance == null) throw new MissingMethodException("EventPacket.RecvAttendanceResponse(IMessage,int,int)");
            harmony.Patch(attendance, postfix: new HarmonyMethod(typeof(Plugin), nameof(AttendanceRewards)));
            Patch(typeof(IntroUI), "Awake", nameof(IntroAwake), true, 0);
            EnsureCommerceHost();
            Logger.LogInfo("Cash shop runtime installed: persistent catalog host and server-owned billing hooks");
        }
        catch (Exception exception)
        {
            products = null;
            Logger.LogError("Cash shop initialization failed: " + exception);
        }
    }

    private void Patch(Type type, string name, string callback, bool postfix, int count)
    {
        MethodInfo target = type.GetMethods(All).Single(method => method.IsGameMethod(name) && method.GetParameters().Length == count);
        var patch = new HarmonyMethod(typeof(Plugin), callback);
        harmony.Patch(target, prefix: postfix ? null : patch, postfix: postfix ? patch : null);
    }

    private void PatchGeneratedReceiver(string readableName, string callback)
    {
        // Compiler-generated declaring types are found by their mapped methods;
        // neither an obfuscated type name nor a generated class number is pinned.
        MethodInfo target = typeof(EventPacket).GetNestedTypes(All)
            .SelectMany(type => type.GetMethods(All | BindingFlags.DeclaredOnly))
            .Single(method => method.IsGameMethod(readableName) && method.ReturnType == typeof(bool) &&
                method.GetParameters().Select(parameter => parameter.ParameterType)
                    .SequenceEqual(new[] { typeof(byte[]), typeof(int), typeof(int) }));
        harmony.Patch(target, postfix: new HarmonyMethod(typeof(Plugin), callback));
    }

    private static string Origin()
    {
        var uri = typeof(ServerURLInfo).GetGameField(nameof(ServerURLInfo.MaintenanceUri), All).GetValue(null) as Uri;
        if (uri == null || uri.UserInfo.Length != 0 || uri.Scheme != "http" && uri.Scheme != "https")
            throw new InvalidOperationException("Commerce server origin unavailable");
        return uri.GetLeftPart(UriPartial.Authority);
    }

    private static void IntroAwake() => EnsureCommerceHost();

    private static void EnsureCommerceHost()
    {
        if (applicationQuitting || host != null && host.gameObject.activeInHierarchy) return;
        if (host != null) UnityEngine.Object.Destroy(host.gameObject);
        var owner = new GameObject("BD2 Cash Shop Host");
        UnityEngine.Object.DontDestroyOnLoad(owner);
        host = owner.AddComponent<CommerceHost>();
        Log?.LogInfo("Commerce runtime host created: reason=startup-or-intro-restoration");
        host.StartCoroutine(RefreshCatalog());
    }

    public sealed class CommerceHost : MonoBehaviour
    {
        private void OnApplicationQuit() { applicationQuitting = true; }
        private void OnDestroy()
        {
            if (ReferenceEquals(host, this)) host = null;
            Log?.LogInfo("Commerce runtime host stopped: reason=" + (applicationQuitting ? "application-quit" : "scene-destruction-awaiting-intro-restoration"));
        }
    }

    private static IEnumerator RefreshCatalog()
    {
        float nextFetch = 0;
        while (true)
        {
            string origin = null;
            try { origin = Origin(); } catch { }
            if (origin != catalogOrigin) { products = null; catalogOrigin = origin; nextFetch = 0; RefreshPrices(); }
            if (origin != null && Time.realtimeSinceStartup >= nextFetch)
            {
                Task<Dictionary<string, CommerceProduct>> request = Task.Run(() => Fetch(origin));
                while (!request.IsCompleted)
                {
                    string current = null;
                    try { current = Origin(); } catch { }
                    if (current != origin) { products = null; }
                    yield return null;
                }
                string after = null;
                try { after = Origin(); } catch { }
                if (request.IsFaulted) { products = null; Log?.LogWarning("Commerce catalog unavailable: " + request.Exception.GetBaseException().Message); }
                else if (after == origin) { products = request.Result; Log?.LogInfo("Loaded commerce policy for " + products.Count + " SKUs"); }
                RefreshPrices();
                nextFetch = Time.realtimeSinceStartup + (products == null ? 3 : 30);
            }
            yield return new WaitForSecondsRealtime(1);
        }
    }

    private static Dictionary<string, CommerceProduct> Fetch(string origin)
    {
        var request = (HttpWebRequest)WebRequest.Create(origin + "/client/commerce");
        request.Method = "GET";
        request.Timeout = 10000;
        request.ReadWriteTimeout = 10000;
        request.AllowAutoRedirect = false;
        string proxy = Environment.GetEnvironmentVariable("BD2_CLIENT_PROXY_URL");
        if (new Uri(origin).IsLoopback || string.IsNullOrWhiteSpace(proxy)) request.Proxy = null;
        else
        {
            if (!Uri.TryCreate(proxy, UriKind.Absolute, out var proxyUri) || proxyUri.Scheme != "http" || proxyUri.UserInfo.Length != 0 || proxyUri.AbsolutePath != "/" || proxyUri.Query.Length != 0 || proxyUri.Fragment.Length != 0)
                throw new InvalidOperationException("Unsupported client commerce proxy");
            request.Proxy = new WebProxy(proxyUri);
        }
        using (var response = (HttpWebResponse)request.GetResponse())
        using (var stream = response.GetResponseStream())
        using (var reader = new StreamReader(stream))
        {
            if (response.StatusCode != HttpStatusCode.OK) throw new IOException("Commerce HTTP status " + response.StatusCode);
            var bodyBuilder = new System.Text.StringBuilder();
            var buffer = new char[8192];
            int read;
            while ((read = reader.Read(buffer, 0, buffer.Length)) != 0)
            {
                if (bodyBuilder.Length + read > 4 * 1024 * 1024) throw new IOException("Commerce catalog too large");
                bodyBuilder.Append(buffer, 0, read);
            }
            string body = bodyBuilder.ToString();
            return JsonConvert.DeserializeObject<CommerceCatalog>(body).Validate(Bd2Build.Versions.Game);
        }
    }

    private static CommerceProduct Resolve(string sku)
    {
        try
        {
            if (products == null || catalogOrigin != Origin() || sku == null) return null;
            CashProductTable table = CashShopInfo.GetCashProductDTO(sku, SG.App.MarketType);
            if (table == null || !products.TryGetValue(CommerceProduct.MakeKey(table.GroupId, table.Id, table.SaleGroup, sku), out var policy)) return null;
            if (table == null || table.PriceType != 1 || table.GroupId != policy.GroupId || table.Id != policy.ProductId || table.SaleGroup != policy.SaleGroup ||
                CashShopInfo.GetProductIdByMarket(table, SG.App.MarketType) != sku) return null;
            return policy;
        }
        catch { return null; }
    }

    private static CommerceProduct Resolve(int group, int id, int sale)
    {
        try
        {
            var table = CashShopInfo.GetCashProductDTO(group, id, sale);
            if (table == null || table.PriceType != 1 || products == null || catalogOrigin != Origin()) return null;
            string sku = CashShopInfo.GetProductIdByMarket(table, SG.App.MarketType);
            return products.TryGetValue(CommerceProduct.MakeKey(group, id, sale, sku), out var policy) ? policy : null;
        }
        catch { return null; }
    }

    private static bool Purchase(string __0, object __1, Action __2)
    {
        var policy = Resolve(__0);
        if (policy == null || !policy.Enabled)
        {
            __2?.Invoke();
            ShowPurchaseFailure();
            return false;
        }
        if (!HasFunds(policy))
        {
            __2?.Invoke();
            InsufficientFunds(policy);
            return false;
        }
        long pay = System.Threading.Interlocked.Increment(ref paySequence);
        var result = new PurchaseData(__0, pay, "bd2-local:" + pay, CommercePayments.Receipt(pay, policy));
        (__1 as Delegate)?.DynamicInvoke(result);
        return false;
    }

    private static bool Suppress() => false;
    private static bool ProductPrefetch(Action __3) { __3?.Invoke(); return false; }
    private static bool LocalPriceLocalization(Action __1)
    {
        Log?.LogInfo("Commerce SDK suppressed: operation=price-localization reason=server-owned-cash-shop");
        __1?.Invoke();
        return false;
    }
    private static bool SkipOfficialBilling()
    {
        Log?.LogInfo("Commerce SDK suppressed: operation=gpg-billing-initialization reason=server-owned-cash-shop");
        return false;
    }
    private static bool LocalBillingStatus(Action<bool?> __0)
    {
        Log?.LogInfo("Commerce SDK suppressed: operation=gpg-oauth-status reason=server-owned-cash-shop");
        __0?.Invoke(false);
        return false;
    }
    private static bool LocalSplitPrices(Action __2)
    {
        Log?.LogInfo("Commerce SDK suppressed: operation=gpg-split-price-localization reason=server-owned-cash-shop");
        __2?.Invoke();
        return false;
    }
    private static bool PriceNumber(ref string __result) { __result = "0"; return false; }
    private static bool CurrencyCode(ref string __result) { __result = "BD2"; return false; }

    private static bool BeforeRequest(int __0, int __1, int __2, long __3, string __5, ref Action __7, Action<int> __8, ref Action __9)
    {
        CommerceProduct quote = Resolve(__0, __1, __2);
        if (quote == null || !quote.Enabled || quote.Sku != __5)
        {
            __8?.Invoke(1);
            ShowPurchaseFailure();
            return false;
        }
        if (!HasFunds(quote))
        {
            __8?.Invoke(1);
            InsufficientFunds(quote);
            return false;
        }
        // Cash products retain PriceType=Cash, so the original success handler has
        // no local subtraction. Apply the accepted quote once, only after success.
        Action continuation = __7;
        long payment = __3;
        string origin = catalogOrigin;
        object user = CommonPacket.UserInfo;
        __7 = CommercePayments.AcceptOnce(payment, () => origin == catalogOrigin && ReferenceEquals(user, CommonPacket.UserInfo), delegate
        {
            if (quote.Amount > 0)
            {
                CommonPacket.RemoveHaveCurrency((EElementType)quote.ItemType, quote.Amount, false, true);
                UIManager.UpdateUseCurrency((EElementType)quote.ItemType, ECurrencyDirectDisplayType.FadeOutOnlySubtractionCurrency, quote.Amount, 0, null, false);
            }
        }, continuation);
        // The server has already supplied the real ticket expiry. Prevent the
        // client's additional +30-day estimate and reload authoritative claims.
        Action bought = __9;
        __9 = delegate
        {
            bought?.Invoke();
            if (origin == catalogOrigin && ReferenceEquals(user, CommonPacket.UserInfo) && attendanceRefreshPayments.Add(payment))
                RefreshAttendanceIndependent();
        };
        return true;
    }

    private static bool HasFunds(CommerceProduct quote) => quote.Amount == 0 ||
        CommonPacket.UserInfo != null && CommonPacket.GetHaveCurrencyCount((EElementType)quote.ItemType, false) >= quote.Amount;

    private static void ShowPurchaseFailure() =>
        UIManager.OpenSelectiveMessagePopupUI(Unavailable, "购买失败", MessagePopupUI.EMessagePopupButtonStyle.ONLY_OK);

    private static bool SuppressExternalPaymentConfirmation(string __1, string __2)
    {
        // Only the official cash checkout prompt is removed. Its callers proceed
        // synchronously to our Purchase prefix, which opens the failure dialog
        // for disabled goods or performs the server-owned purchase for enabled goods.
        return __1 != LocalTextInfo.GetText(13104) || __2 != LocalTextInfo.GetText(13103);
    }

    private static void InsufficientFunds(CommerceProduct quote)
    {
        if (quote.ItemType == 2 || quote.ItemType == 3)
            UIManager.OpenCashShopLinkPopupUI((EElementType)quote.ItemType, 0, ShopLinkPopupUI.EExitFieldMapType.None, null, null, null);
        else UIManager.OpenSelectiveMessagePopupUI(LocalTextInfo.GetText(154), "", MessagePopupUI.EMessagePopupButtonStyle.ONLY_OK);
    }

    private static bool PriceLabel(string __0, ref string __result)
    {
        var policy = Resolve(__0);
        string text = policy == null || !policy.Enabled ? Unavailable : policy.ItemType == 0 ? "Free" : policy.Amount.ToString(CultureInfo.InvariantCulture);
        __result = Marker + Uri.EscapeDataString(__0 ?? "") + ">" + text + "</link>";
        return false;
    }

    private static CommerceProduct FromLabel(string label)
    {
        if (label == null) return null;
        int start = label.IndexOf(Marker, StringComparison.Ordinal);
        if (start < 0) return null;
        start += Marker.Length;
        int end = label.IndexOf('>', start);
        return end < 0 ? null : Resolve(Uri.UnescapeDataString(label.Substring(start, end - start)));
    }

    private static bool SetCash(CurrencyButton __instance, string __0)
    {
        CommercePriceLayout.Reset(__instance.transform as RectTransform);
        RestorePricePrefixes(__instance);
        if (__0 == null || !__0.Contains(Marker)) return true;
        string sku = ExtractSku(__0);
        priceViews[__instance] = () => Render(__instance, Resolve(sku));
        Render(__instance, FromLabel(__0));
        return false;
    }

    private static void Render(CurrencyButton button, CommerceProduct policy)
    {
        if (button == null) return;
        RestorePricePrefixes(button);
        button.SetActive(true);
        if (policy == null || !policy.Enabled || policy.ItemType == 0)
        {
            button.SetCash(policy != null && policy.Enabled ? "Free" : Unavailable);
            return;
        }
        button.Set((EElementType)policy.ItemType, 0, policy.Amount);
        button.TextPrice.text = PriceText(policy);
        button.SetEnough(CommonPacket.GetHaveCurrencyCount((EElementType)policy.ItemType, false) >= policy.Amount);
    }

    private static void RestorePricePrefixes(CurrencyButton button)
    {
        if (button == null || !unavailablePriceLabels.TryGetValue(button, out var saved)) return;
        foreach (var entry in saved)
            if (entry.Key != null)
            {
                entry.Key.text = entry.Value.Text;
                entry.Key.gameObject.SetActive(entry.Value.Active);
                if (entry.Value.Layout)
                {
                    entry.Key.alignment = entry.Value.Alignment;
                    entry.Key.enableWordWrapping = entry.Value.Wrap;
                    RectTransform rect = entry.Key.rectTransform;
                    rect.anchorMin = entry.Value.AnchorMin; rect.anchorMax = entry.Value.AnchorMax;
                    rect.pivot = entry.Value.Pivot; rect.offsetMin = entry.Value.OffsetMin; rect.offsetMax = entry.Value.OffsetMax;
                    if (entry.Value.Drivers != null)
                        foreach (var driver in entry.Value.Drivers) if (driver.Key != null) driver.Key.enabled = driver.Value;
                }
            }
        unavailablePriceLabels.Remove(button);
    }

    private static void PopupPrice(CashShopBuyPopupUI __instance, EElementType __3, string __7)
    {
        CurrencyButton button = Field<CurrencyButton>(__instance, "_currencyButton");
        if (button == null) return;
        CommercePriceLayout.Reset(button.transform as RectTransform);
        RestorePricePrefixes(button);
        priceViews.Remove(button);
        if (__3 != EElementType.Cash) return;
        string sku = __7;
        priceViews[button] = () => RefreshPopupPrice(__instance, button, Resolve(sku));
        RefreshPopupPrice(__instance, button, Resolve(sku));
    }

    private static string PriceText(CommerceProduct policy)
    {
        string amount = policy.Amount.ToString("N0", CultureInfo.InvariantCulture);
        return policy.ItemType == (int)EElementType.Jewelry
            ? amount + " <size=70%><color=#E3B01F>" + LocalTextInfo.GetText(2560) + "</color></size>"
            : amount;
    }

    private static void RefreshPopupPrice(CashShopBuyPopupUI popup, CurrencyButton button, CommerceProduct policy)
    {
        CommercePriceLayout.Reset(button.transform as RectTransform);
        Render(button, policy);
        if (policy != null && policy.Enabled && policy.ItemType != 0)
            CommercePriceLayout.Apply(button.transform as RectTransform, Field<UISprite>(button, "_spritePrice"), button.TextPrice);
        ShowUnavailablePopupPrice(popup, button, policy);
    }

    private static void ShowUnavailablePopupPrice(CashShopBuyPopupUI popup, CurrencyButton button, CommerceProduct policy)
    {
        if (policy != null && policy.Enabled) return;
        // CurrencyParent is the gray price strip. Never search its parent,
        // which also contains the separate confirmation and cancellation buttons.
        Transform container = button.transform;
        GameObject buy = Field<GameObject>(popup, "_objBuyButton"), cancel = Field<GameObject>(popup, "_objCancelButton");
        TMP_Text price = button.TextPrice;
        var saved = new Dictionary<TMP_Text, PriceLabelState>();
        foreach (var label in container.GetComponentsInChildren<TMP_Text>(true))
        {
            if (label == price || buy != null && label.transform.IsChildOf(buy.transform) || cancel != null && label.transform.IsChildOf(cancel.transform)) continue;
            if (label.text != "购买费用") continue;
            RectTransform rect = label.rectTransform;
            saved[label] = new PriceLabelState { Text = label.text, Active = label.gameObject.activeSelf,
                Layout = true, Alignment = label.alignment, Wrap = label.enableWordWrapping,
                AnchorMin = rect.anchorMin, AnchorMax = rect.anchorMax, Pivot = rect.pivot,
                OffsetMin = rect.offsetMin, OffsetMax = rect.offsetMax };
            var drivers = new Dictionary<Behaviour, bool>();
            foreach (var group in container.GetComponentsInChildren<UnityEngine.UI.LayoutGroup>(true))
            { drivers[group] = group.enabled; group.enabled = false; }
            foreach (var fitter in container.GetComponentsInChildren<UnityEngine.UI.ContentSizeFitter>(true))
            { drivers[fitter] = fitter.enabled; fitter.enabled = false; }
            saved[label].Drivers = drivers;
            label.text = Unavailable;
            label.gameObject.SetActive(true);
            label.alignment = TextAlignmentOptions.Center;
            label.enableWordWrapping = false;
            rect.anchorMin = Vector2.zero; rect.anchorMax = Vector2.one; rect.pivot = new Vector2(0.5f, 0.5f);
            rect.offsetMin = Vector2.zero; rect.offsetMax = Vector2.zero;
        }
        if (saved.Count == 0)
        {
            Log?.LogWarning("Commerce price layout: fee label missing inside CurrencyParent; centering existing price label");
            if (price == null) return;
            RectTransform rect = price.rectTransform;
            saved[price] = new PriceLabelState { Text = price.text, Active = price.gameObject.activeSelf,
                Layout = true, Alignment = price.alignment, Wrap = price.enableWordWrapping,
                AnchorMin = rect.anchorMin, AnchorMax = rect.anchorMax, Pivot = rect.pivot,
                OffsetMin = rect.offsetMin, OffsetMax = rect.offsetMax };
            var drivers = new Dictionary<Behaviour, bool>();
            foreach (var group in container.GetComponentsInChildren<UnityEngine.UI.LayoutGroup>(true))
            { drivers[group] = group.enabled; group.enabled = false; }
            foreach (var fitter in container.GetComponentsInChildren<UnityEngine.UI.ContentSizeFitter>(true))
            { drivers[fitter] = fitter.enabled; fitter.enabled = false; }
            saved[price].Drivers = drivers;
            price.text = Unavailable;
            price.gameObject.SetActive(true);
            price.alignment = TextAlignmentOptions.Center;
            price.enableWordWrapping = false;
            rect.anchorMin = Vector2.zero; rect.anchorMax = Vector2.one; rect.pivot = new Vector2(0.5f, 0.5f);
            rect.offsetMin = Vector2.zero; rect.offsetMax = Vector2.zero;
            unavailablePriceLabels[button] = saved;
            return;
        }
        if (price != null)
        {
            saved[price] = new PriceLabelState { Text = price.text, Active = price.gameObject.activeSelf };
            price.gameObject.SetActive(false);
        }
        unavailablePriceLabels[button] = saved;
    }

    private static T Field<T>(object target, string name)
    {
        for (Type type = target.GetType(); type != null; type = type.BaseType)
        {
            FieldInfo field = type.GetGameField(name, All | BindingFlags.DeclaredOnly);
            if (field != null) return (T)field.GetValue(target);
        }
        throw new MissingFieldException(target.GetType().FullName, name);
    }

    private static void PackagePrice(PackagePurchaseButton __instance)
    {
        priceViews.Remove(__instance);
        int group = Field<int>(__instance, "_productGroupId"), id = Field<int>(__instance, "_productId"), sale = Field<int>(__instance, "_productSaleGroupId");
        var table = CashShopInfo.GetCashProductDTO(group, id, sale);
        if (table == null || table.PriceType != 1) return;
        var button = Field<CurrencyButton>(__instance, "_btnCurrency");
        priceViews[__instance] = () => PackagePrice(__instance);
        CommerceProduct policy = Resolve(group, id, sale);
        TMP_Text label = Field<TMP_Text>(__instance, "_textPrice");
        RestorePricePrefixes(button);
        if (policy == null || !policy.Enabled)
        {
            if (button != null) button.SetActive(false);
            if (label != null) { label.text = Unavailable; label.gameObject.SetActive(true); }
            return;
        }
        Render(button, policy);
        if (button != null) label?.gameObject.SetActive(false);
    }

    private static void PassPrice(PassRootUI __instance, bool __result)
    {
        priceViews.Remove(__instance);
        if (!__result) return;
        var table = PassInfo.GetPassBuyTable(Field<int>(__instance, "_curPassTableID"), Define_PassBuyType.PbPremium2);
        priceViews[__instance] = () => PassPrice(__instance, true);
        Render(Field<UISprite>(__instance, "_imgPremiumBuyPrice"), Field<TMP_Text>(__instance, "_txtPremiumBuyPrice"), Resolve(table.CashProductGroupId, table.CashProductId, table.CashSalesGroup));
    }

    private static void PassPopupPrice(BuyConfirmPopupUI __instance, int __0, Define_PassBuyType __1)
    {
        var price = Field<TMP_Text>(__instance, "_textCurrency");
        CommercePriceLayout.Reset(price?.transform.parent as RectTransform);
        priceViews.Remove(__instance);
        if (__1 != Define_PassBuyType.PbPremium2) return;
        var table = PassInfo.GetPassBuyTable(__0, __1);
        int pass = __0;
        Define_PassBuyType buy = __1;
        priceViews[__instance] = () => PassPopupPrice(__instance, pass, buy);
        var policy = Resolve(table.CashProductGroupId, table.CashProductId, table.CashSalesGroup);
        var sprite = Field<UISprite>(__instance, "_uiSpriteCurrency");
        Render(sprite, price, policy);
        if (policy != null && policy.Enabled && policy.ItemType != 0)
            CommercePriceLayout.Apply(price.transform.parent as RectTransform, sprite, price);
    }

    private static void Render(UISprite sprite, TMP_Text text, CommerceProduct policy)
    {
        if (sprite != null)
        {
            sprite.gameObject.SetActive(policy != null && policy.Enabled && policy.ItemType != 0);
            if (policy != null && policy.Enabled && policy.ItemType != 0) sprite.SetSprite(CurrencyInfo.GetCurrencyDTO((EElementType)policy.ItemType).IconSpriteNameSmall);
        }
        if (text != null) text.text = policy == null || !policy.Enabled ? Unavailable : policy.ItemType == 0 ? "Free" : PriceText(policy);
    }

    private static void GachaPrice(GachaProductButtonElement __instance, string __4)
    {
        var buttons = Field<CurrencyButton[]>(__instance, "_currencyGachas");
        if (buttons == null || buttons.Length == 0) return;
        foreach (var button in buttons) if (button != null) priceViews.Remove(button);
        if (__4 == null || !__4.Contains(Marker)) return;
        string sku = ExtractSku(__4);
        priceViews[buttons[0]] = () => Render(buttons[0], Resolve(sku));
        foreach (var button in buttons) button?.SetActive(false);
        Render(buttons[0], FromLabel(__4));
        Field<TMP_Text>(__instance, "_textDescription").text = "";
    }

    private static void AttendanceRewards(IMessage __0, int __2, bool __result)
    {
        if (__2 != 0 || !__result) return;
        try
        {
            if (!AttendanceRewardEnvelope.TryRead(__0, out ByteString encoded, out string receipt)) return;
            RewardDBInfoBundle bundle = RewardDBInfoBundle.Parser.ParseFrom(encoded);
            string key = Origin() + "/" + receipt;
            if (!attendanceReceipts.Add(key)) return;
            var rewards = new List<ItemBaseInfo>();
            DataManager.AddRewardInfoBundle(bundle, ref rewards, true);
            UIManager.RefreshCurrencyUI();
            UIManager.OpenCharNoticeUI(bundle.CharInfo, bundle.ItemAutoExchangeInfo, bundle.ItemAutoUpgradeInfo);
        }
        catch (Exception error) { Log?.LogWarning("Attendance commerce rewards could not be applied: " + error.Message); }
    }

    private static void LoginPassRewards(byte[] __0, int __2, bool __result)
    {
        if (__2 == 0 && __result) ApplyRewardEnvelope(AttendanceResponse.Parser.ParseFrom(__0));
    }

    private static void EventRewards(byte[] __0, int __2, bool __result)
    {
        if (__2 == 0 && __result) ApplyRewardEnvelope(EventRewardResponse.Parser.ParseFrom(__0));
    }

    private static void ApplyRewardEnvelope(IMessage message) => AttendanceRewards(message, 0, true);

    private static void RefreshAttendanceIndependent()
    {
        // Own the receiver rather than setting EventPacket's single static
        // onAttendanceRespSuccess slot, which may belong to an in-flight UI.
        string origin = catalogOrigin;
        object user = CommonPacket.UserInfo;
        SG.Net.Send(new AttendanceRequest { Seq = BDNetwork.Sequence.ReqSeq }, (data, packetCode, error) =>
        {
            if (error != 0 || origin != catalogOrigin || !ReferenceEquals(user, CommonPacket.UserInfo)) return false;
            AttendanceResponse response = AttendanceResponse.Parser.ParseFrom(data);
            CashShopPacket.RefreshAllAttendanceInfo(response.AttendanceInfo);
            CashShopPacket.RefreshSubscribeAttendanceInfo(response.SubscribeAttendanceInfo);
            CashShopPacket.RefreshAttendancePackageInfo(response.AttendancePackageInfo);
            CashShopPacket.RefreshLoginPassPackageInfo(response.LoginPassPackageInfo);
            ApplyRewardEnvelope(response);
            return true;
        }, false, null, false, null);
    }

    private static string ExtractSku(string label)
    {
        int start = label.IndexOf(Marker, StringComparison.Ordinal) + Marker.Length;
        int end = label.IndexOf('>', start);
        return end < 0 ? null : Uri.UnescapeDataString(label.Substring(start, end - start));
    }

    private static void RefreshPrices()
    {
        foreach (var view in priceViews.ToArray())
        {
            if (view.Key == null) { priceViews.Remove(view.Key); continue; }
            try { view.Value(); } catch (Exception error) { Log?.LogWarning("Commerce UI refresh: " + error.Message); }
        }
    }

    private void OnDestroy()
    {
        // Unity may remove the BepInEx component during startup. Static hooks and
        // the separate persistent host must survive that scene cleanup.
        Log?.LogInfo("Cash shop plugin component destroyed: billing hooks retained on persistent runtime");
    }
}
