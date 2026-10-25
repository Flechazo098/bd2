using System;
using System.Linq;
using System.Reflection;
using BD2.GameNames;
using Cysharp.Threading.Tasks;
using TMPro;
using UnityEngine;
using UnityEngine.EventSystems;
using UnityEngine.UI;

namespace Bd2FBattle;

// Templates and clone helpers for the game's own widgets.  Every clone is made
// under an inactive holder so game scripts can be removed before they wake up.
internal sealed class NativeActionControls
{
    // The battle list lays its cards out at this size in landscape.
    internal static readonly Vector2 CardSize = new(280, 90);

    internal GameObject CardTemplate { get; }
    internal GameObject SlotTemplate { get; }
    internal Vector2 SlotSize { get; }

    internal NativeActionControls(GameObject battlePrefab, CharManageUI editor)
    {
        CardTemplate = (battlePrefab.GetComponentInChildren<BattleUIComponent_CharActionSelectItem>(true) ??
            throw new MissingMemberException("Native battle action card template unavailable.")).gameObject;
        CharManageLoopScrollItem[] items = editor.GetComponentsInChildren<CharManageLoopScrollItem>(true);
        CharManageLoopScrollItem item = items.FirstOrDefault(entry => entry.gameObject.activeInHierarchy) ?? items.FirstOrDefault() ??
            throw new MissingMemberException("Native character list card unavailable.");
        SlotTemplate = item.gameObject;
        Vector2 size = ((RectTransform)item.transform).rect.size;
        SlotSize = size.x > 10 && size.y > 10 ? size : new Vector2(120, 224);
    }

    internal static T Field<T>(object target, string name) where T : class => target == null ? null :
        target.GetType().GetGameField(name, BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetValue(target) as T;

    internal static int Integer(object target, string name) => (int)(target.GetType().GetGameField(name,
        BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetValue(target) ?? throw new MissingFieldException(target.GetType().Name, name));

    internal static void LoadBattlePrefab(Action<AddressableAsset<GameObject>> ready, Action<Exception> failed)
    {
        MethodInfo method = typeof(AssetManager).GetGameMethod("GetAssetWithFailure", BindingFlags.Public | BindingFlags.Static) ??
            throw new MissingMethodException(nameof(AssetManager), "GetAssetWithFailure");
        Action<AddressableAsset<GameObject>> complete = async asset => { await UniTask.SwitchToMainThread(); ready(asset); };
        Action<Exception> failure = async error => { await UniTask.SwitchToMainThread(); failed(error); };
        method.MakeGenericMethod(typeof(GameObject)).Invoke(null, ["UIPrefabs/BattleUI_FieldBattle.prefab", complete, failure]);
    }

    // Copies source without running any of its scripts.  Call Wake once the
    // caller has read what it needs from the copy and removed the rest.
    internal static GameObject Dormant(GameObject source, string name)
    {
        var holder = new GameObject("FBattle Dormant");
        holder.SetActive(false);
        GameObject copy = UnityEngine.Object.Instantiate(source, holder.transform, false);
        copy.name = name;
        copy.SetActive(true);
        return copy;
    }

    internal static RectTransform Wake(GameObject copy, Transform parent)
    {
        Transform holder = copy.transform.parent;
        copy.transform.SetParent(parent, false);
        if (holder != null && holder.childCount == 0) UnityEngine.Object.Destroy(holder.gameObject);
        foreach (CanvasGroup group in copy.GetComponents<CanvasGroup>()) { group.alpha = 1; group.interactable = true; group.blocksRaycasts = true; }
        return (RectTransform)copy.transform;
    }

    // Input, localisation and list scripts would fight the editor; graphics,
    // layout and effect scripts stay so the copy keeps the game's look.
    internal static void Strip(GameObject copy, Func<MonoBehaviour, bool> alsoRemove = null)
    {
        foreach (MonoBehaviour script in copy.GetComponentsInChildren<MonoBehaviour>(true))
        {
            if (script == null) continue;
            if (script is LocalTextConverter || script is MultiPlatformInputBinder || script is LoopScrollItemBase ||
                script is IEventSystemHandler || alsoRemove?.Invoke(script) == true)
                UnityEngine.Object.DestroyImmediate(script);
        }
    }

    internal static RectTransform Rect(string name, Transform parent, float x, float y, float width, float height)
    {
        var child = new GameObject(name, typeof(RectTransform));
        return Place(child.transform, parent, x, y, width, height);
    }

    internal static RectTransform Place(Transform child, Transform parent, float x, float y, float width, float height)
    {
        child.SetParent(parent, false);
        foreach (Behaviour component in child.GetComponents<Behaviour>()) if (IsLayout(component)) component.enabled = false;
        var rect = (RectTransform)child;
        rect.localScale = Vector3.one;
        rect.localRotation = Quaternion.identity;
        rect.anchorMin = rect.anchorMax = rect.pivot = new Vector2(.5f, .5f);
        rect.anchoredPosition = new Vector2(x, y);
        rect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, width);
        rect.SetSizeWithCurrentAnchors(RectTransform.Axis.Vertical, height);
        IgnoreLayout(rect);
        return rect;
    }

    internal static void IgnoreLayout(Component target)
    {
        LayoutElement layout = target.GetComponent<LayoutElement>() ?? target.gameObject.AddComponent<LayoutElement>();
        layout.ignoreLayout = true;
    }

    internal static bool IsLayout(Behaviour component) => component is LayoutGroup || component is ContentSizeFitter ||
        component.GetType().Namespace == "TheraBytes.BetterUi" && component is not Graphic && component is not Selectable;

    internal static TMP_Text Label(TMP_Text template, Transform parent, string name, string value, float x, float y,
        float width, float height = 30, float size = 22, TextAlignmentOptions alignment = TextAlignmentOptions.MidlineLeft)
    {
        GameObject copy = Dormant(template.gameObject, name);
        Strip(copy);
        TMP_Text label = copy.GetComponent<TMP_Text>();
        Place(Wake(copy, parent), parent, x, y, width, height);
        label.text = value;
        // The popup fades its own title in; a copy taken mid-fade would stay invisible.
        label.alpha = 1;
        label.canvasRenderer.SetAlpha(1);
        label.enableAutoSizing = true;
        label.fontSizeMax = size;
        label.fontSizeMin = Math.Min(14, size);
        label.alignment = alignment;
        label.enableWordWrapping = true;
        label.overflowMode = TextOverflowModes.Ellipsis;
        label.margin = Vector4.zero;
        label.richText = false;
        label.raycastTarget = false;
        return label;
    }

    // A game button with its own title, its click rerouted to the editor.
    internal static Button Button(GameObject template, Transform parent, string name, string title, Vector2 size, Action click)
    {
        GameObject copy = Dormant(template, name);
        Selectable look = copy.GetComponent<Selectable>();
        Selectable.Transition transition = look == null ? Selectable.Transition.None : look.transition;
        AnimationTriggers triggers = look?.animationTriggers;
        ColorBlock colors = look == null ? ColorBlock.defaultColorBlock : look.colors;
        SpriteState sprites = look == null ? default : look.spriteState;
        Graphic surface = look?.targetGraphic;
        Strip(copy, script => script is Selectable);
        foreach (Behaviour component in copy.GetComponentsInChildren<Behaviour>(true)) if (IsLayout(component)) component.enabled = false;
        foreach (TMP_Text label in copy.GetComponentsInChildren<TMP_Text>(true))
        {
            // Shrink long titles but never grow past the button's own size.
            float native = label.enableAutoSizing ? label.fontSizeMax : label.fontSize;
            label.text = title;
            label.enableAutoSizing = true;
            label.fontSizeMax = native;
            label.fontSizeMin = Math.Min(14, native);
        }
        RectTransform rect = Place(Wake(copy, parent), parent, 0, 0, size.x, size.y);
        if (surface == null) surface = copy.GetComponent<Graphic>() ?? copy.GetComponentsInChildren<Graphic>(true).FirstOrDefault(graphic => graphic is not TMP_Text);
        if (surface != null) surface.raycastTarget = true;
        Button button = copy.AddComponent<Button>();
        // Keep the game's press feedback (its animator triggers or tints).
        button.transition = transition;
        if (triggers != null) button.animationTriggers = triggers;
        button.colors = colors;
        button.spriteState = sprites;
        button.targetGraphic = surface;
        button.onClick.AddListener(() => click());
        rect.gameObject.SetActive(true);
        return button;
    }

    internal static Image Fill(Transform parent, string name, Color color, float x, float y, float width, float height)
    {
        Image image = Rect(name, parent, x, y, width, height).gameObject.AddComponent<Image>();
        image.color = color;
        image.raycastTarget = false;
        return image;
    }

    internal static T Scroll<T>(Transform parent, string name, float x, float y, float width, float height, bool horizontal) where T : ScrollRect
    {
        RectTransform root = Rect(name, parent, x, y, width, height);
        Image surface = root.gameObject.AddComponent<Image>();
        surface.color = Color.clear;
        root.gameObject.AddComponent<RectMask2D>();
        T scroll = root.gameObject.AddComponent<T>();
        scroll.viewport = root;
        scroll.horizontal = horizontal;
        scroll.vertical = !horizontal;
        scroll.movementType = ScrollRect.MovementType.Clamped;
        scroll.scrollSensitivity = 40;
        RectTransform content = Rect(name + " Content", root, 0, 0, width, height);
        content.anchorMin = content.anchorMax = content.pivot = new Vector2(0, 1);
        content.anchoredPosition = Vector2.zero;
        scroll.content = content;
        return scroll;
    }
}

// A row of cards inside the character list: the wheel and vertical drags go
// to the list, horizontal drags page through the cards.
[System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
internal sealed class RowScroll : ScrollRect
{
    internal ScrollRect Outer;
    private bool outerDrag;

    private bool Overflows => content != null && viewport != null && content.rect.width > viewport.rect.width + 1;

    public override void OnScroll(PointerEventData data)
    {
        if (Outer != null) Outer.OnScroll(data);
    }

    public override void OnInitializePotentialDrag(PointerEventData data)
    {
        Outer?.OnInitializePotentialDrag(data);
        base.OnInitializePotentialDrag(data);
    }

    public override void OnBeginDrag(PointerEventData data)
    {
        outerDrag = !Overflows || Mathf.Abs(data.delta.y) > Mathf.Abs(data.delta.x);
        if (outerDrag) Outer?.OnBeginDrag(data);
        else base.OnBeginDrag(data);
    }

    public override void OnDrag(PointerEventData data)
    {
        if (outerDrag) Outer?.OnDrag(data);
        else base.OnDrag(data);
    }

    public override void OnEndDrag(PointerEventData data)
    {
        if (outerDrag) Outer?.OnEndDrag(data);
        else base.OnEndDrag(data);
        outerDrag = false;
    }
}

// Releases a texture the game loaded for one of the editor's images.
internal sealed class TextureLease : MonoBehaviour
{
    private AddressableAsset<Texture> asset;
    private int generation;

    internal void Load(RawImage image, int costumeDesignId, bool skillArt)
    {
        int current = ++generation;
        void loaded(AddressableAsset<Texture> texture)
        {
            if (this == null || image == null || current != generation) { texture.Release(); return; }
            asset?.Release();
            asset = texture;
            image.texture = texture.Asset;
            image.gameObject.SetActive(true);
        }
        if (skillArt) AssetManager.GetCharSkillIllustTexture(costumeDesignId, loaded);
        else AssetManager.GetCharInvenIllustTexture(costumeDesignId, loaded);
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this lifecycle callback.")]
    private void OnDestroy() { generation++; asset?.Release(); asset = null; }
}
