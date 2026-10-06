using System.Collections.Generic;
using TMPro;
using UnityEngine;
using UnityEngine.UI;

namespace Bd2CashShop;

// Own only the price pair. The fee caption and confirmation buttons keep their
// prefab layout; pooled dialogs restore their original geometry on reuse.
internal sealed class CommercePriceLayout : MonoBehaviour
{
    private RectTransform region, icon;
    private TMP_Text price;
    private RectState iconState, textState;
    private bool configured;
    private float fontSize;
    private bool autoSize, wrap;
    private TextAlignmentOptions alignment;
    private readonly List<KeyValuePair<Behaviour, bool>> drivers = [];
    private readonly Vector3[] corners = new Vector3[4];

    internal static void Apply(RectTransform region, UISprite sprite, TMP_Text text)
    {
        if (region == null || sprite == null || text == null) return;
        CommercePriceLayout layout = region.GetComponent<CommercePriceLayout>() ?? region.gameObject.AddComponent<CommercePriceLayout>();
        layout.Restore();
        layout.region = region;
        layout.icon = (RectTransform)sprite.transform;
        layout.price = text;
        layout.iconState = new RectState(layout.icon);
        layout.textState = new RectState(text.rectTransform);
        layout.fontSize = text.fontSize;
        layout.autoSize = text.enableAutoSizing;
        layout.wrap = text.enableWordWrapping;
        layout.alignment = text.alignment;
        // Disable only drivers attached to the pair itself, never the dialog.
        layout.CaptureDrivers(layout.icon);
        layout.CaptureDrivers(text.rectTransform);
        text.enableAutoSizing = false;
        text.enableWordWrapping = false;
        text.alignment = TextAlignmentOptions.MidlineRight;
        layout.configured = true;
        layout.enabled = true;
        layout.Arrange();
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
    internal static void Reset(RectTransform region)
    {
        if (region != null) region.GetComponent<CommercePriceLayout>()?.Restore();
    }

    private void CaptureDrivers(RectTransform target)
    {
        foreach (Behaviour driver in target.GetComponents<Behaviour>())
            if (driver is ContentSizeFitter || driver is AspectRatioFitter)
            {
                drivers.Add(new KeyValuePair<Behaviour, bool>(driver, driver.enabled));
                driver.enabled = false;
            }
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void LateUpdate() => Arrange();
    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnDisable() => Restore();

    private void Arrange()
    {
        if (!configured || region == null || icon == null || price == null || region.rect.width <= 0) return;
        float padding = Mathf.Max(8f, fontSize * 0.35f);
        float left = region.rect.xMin + padding;
        float right = region.rect.xMax - padding;
        float middle = region.rect.center.y;
        // Reserve the actual caption width in this price strip, even after a
        // language, screen size or font change. Decorative labels are ignored.
        foreach (TMP_Text label in region.GetComponentsInChildren<TMP_Text>())
        {
            if (label == price || !label.gameObject.activeInHierarchy || string.IsNullOrWhiteSpace(label.text)) continue;
            label.rectTransform.GetWorldCorners(corners);
            Vector3 a = region.InverseTransformPoint(corners[0]), b = region.InverseTransformPoint(corners[2]);
            if (a.x < region.rect.center.x && a.y <= middle && b.y >= middle)
                left = Mathf.Max(left, a.x + Mathf.Min(label.preferredWidth, b.x - a.x) + padding);
        }
        float available = Mathf.Max(1f, right - left);
        float size = Mathf.Min(fontSize, Mathf.Max(1f, region.rect.height - padding));
        for (int attempt = 0; attempt < 3; attempt++)
        {
            price.fontSize = size;
            float width = price.GetPreferredValues(price.text).x + size + size * 0.25f;
            if (width <= available) break;
            size *= available / width;
        }
        price.fontSize = size;
        float textWidth = price.GetPreferredValues(price.text).x;
        float gap = size * 0.25f;
        // The icon is a square one numeric em high, with a measured gap. This
        // preserves the game's sprite while preventing overlap with any digit.
        Place(price.rectTransform, right - textWidth * 0.5f, middle, textWidth, size * 1.5f);
        Place(icon, right - textWidth - gap - size * 0.5f, middle, size, size);
    }

    private void Place(RectTransform target, float x, float y, float width, float height)
    {
        var parent = target.parent as RectTransform;
        if (parent == null) return;
        Vector3 p = parent.InverseTransformPoint(region.TransformPoint(new Vector3(x, y, 0)));
        Vector3 horizontal = parent.InverseTransformVector(region.TransformVector(Vector3.right));
        Vector3 vertical = parent.InverseTransformVector(region.TransformVector(Vector3.up));
        target.anchorMin = target.anchorMax = new Vector2(0.5f, 0.5f);
        target.pivot = new Vector2(0.5f, 0.5f);
        target.localScale = Vector3.one;
        target.sizeDelta = new Vector2(width * horizontal.magnitude, height * vertical.magnitude);
        target.anchoredPosition = new Vector2(p.x - parent.rect.center.x, p.y - parent.rect.center.y);
    }

    private void Restore()
    {
        if (!configured) return;
        configured = false;
        if (icon != null) iconState.Restore(icon);
        if (price != null)
        {
            textState.Restore(price.rectTransform);
            price.fontSize = fontSize;
            price.enableAutoSizing = autoSize;
            price.enableWordWrapping = wrap;
            price.alignment = alignment;
        }
        foreach (KeyValuePair<Behaviour, bool> driver in drivers) if (driver.Key != null) driver.Key.enabled = driver.Value;
        drivers.Clear();
    }

    private readonly struct RectState
    {
        private readonly Vector2 min, max, pivot, position, size;
        private readonly Vector3 scale;
        internal RectState(RectTransform rect)
        {
            min = rect.anchorMin; max = rect.anchorMax; pivot = rect.pivot;
            position = rect.anchoredPosition; size = rect.sizeDelta; scale = rect.localScale;
        }
        internal readonly void Restore(RectTransform rect)
        {
            rect.anchorMin = min; rect.anchorMax = max; rect.pivot = pivot;
            rect.anchoredPosition = position; rect.sizeDelta = size; rect.localScale = scale;
        }
    }
}
