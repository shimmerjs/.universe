# Friendly MX Master 4 config: the user-facing block over the magicbus
# logiretch source. `universe.home.mxMaster.enable = true` turns the
# daemon module on behind the scenes -- a host never touches
# services/magicbus internals or HID++ control identifiers. Buttons are
# named ("back", "gesture", ...) and remapped to named functions
# ("middle-click", "forward", ...); this module translates them to the
# 0x1B04 CIDs the daemon speaks.
#
# TODAY'S LIMIT (honest): a 0x1B04 remap reassigns a physical button to
# ANOTHER button's native function -- it cannot bind an arbitrary
# keystroke or macro. Arbitrary actions are the deferred divert engine
# (logi-replacement-design.md phase 6a); until it lands, `buttons` is
# button-to-function reassignment only.
{ config, lib, ... }:

let
  cfg = config.universe.home.mxMaster;

  # MX Master 4 physical controls -> 0x1B04 CID (Solaar/logiops +
  # logiretch-0 spike). Decimal with the hex in-comment (nix has no hex
  # int literals).
  buttonCID = {
    top = 196; # 0xC4  -- top button behind the wheel
    middle = 82; # 0x52  -- wheel click
    back = 83; # 0x53  -- rear thumb button
    forward = 86; # 0x56  -- front thumb button
    gesture = 195; # 0xC3  -- thumb gesture button
    thumb = 416; # 0x1A0 -- force/actions button
  };

  # Functions a button can be remapped to (also CIDs: a remap says "act
  # as the control with this CID").
  functionCID = {
    "left-click" = 80; # 0x50
    "right-click" = 81; # 0x51
    "middle-click" = 82; # 0x52
    "back" = 83; # 0x53
    "forward" = 86; # 0x56
    "gesture" = 195; # 0xC3
  };

  buttonNames = builtins.attrNames buttonCID;
  functionNames = builtins.attrNames functionCID;

  buttonsList = lib.mapAttrsToList (btn: fn: {
    cid = buttonCID.${btn};
    remap = functionCID.${fn};
  }) cfg.buttons;
in
{
  options.universe.home.mxMaster = {
    enable = lib.mkEnableOption "Logitech MX Master 4 self-management (battery on the HUD strip + declarative device config; enables the magicbus logiretch source)";

    dpi = lib.mkOption {
      type = lib.types.nullOr (lib.types.ints.between 100 8000);
      default = null;
      description = "Pointer DPI; the device snaps it to its supported steps. null leaves it alone.";
    };

    thumbwheelInvert = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = "Invert the horizontal thumbwheel scroll direction. null leaves it alone.";
    };

    haptic = lib.mkOption {
      type = lib.types.nullOr (lib.types.ints.between 0 100);
      default = null;
      description = "Haptic feedback strength 0-100. null leaves it alone.";
    };

    smartShift = {
      threshold = lib.mkOption {
        type = lib.types.nullOr (lib.types.ints.between 0 50);
        default = null;
        description = "Force to flip the wheel from ratchet to free-spin (1-50; higher = harder). null leaves it alone.";
      };
      mode = lib.mkOption {
        type = lib.types.nullOr (lib.types.ints.between 0 3);
        default = null;
        description = "SmartShift mode (advanced; 0x2111). null leaves it alone.";
      };
      torque = lib.mkOption {
        type = lib.types.nullOr (lib.types.ints.between 0 100);
        default = null;
        description = "Ratchet torque 0-100 (advanced). null leaves it alone.";
      };
    };

    buttons = lib.mkOption {
      type = lib.types.attrsOf (lib.types.enum functionNames);
      default = { };
      example = lib.literalExpression ''{ back = "middle-click"; forward = "forward"; }'';
      description = ''
        Remap named buttons to named functions. Buttons: ${lib.concatStringsSep ", " buttonNames}.
        Functions: ${lib.concatStringsSep ", " functionNames}.
        A remap reassigns a button to another control's native action; arbitrary
        keystrokes/macros are not supported yet (deferred divert engine).
      '';
    };

    resetOnTakeover = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        On (re)connect, reset the mouse's 0x1B04 button state to this config's
        baseline before applying remaps -- clears any divert residue a prior
        driver (e.g. a since-removed Options+) left behind. Leave on unless a
        second HID++ master is deliberately sharing the device.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = builtins.all (b: builtins.elem b buttonNames) (builtins.attrNames cfg.buttons);
        message =
          "universe.home.mxMaster.buttons: unknown button(s) "
          + "${builtins.toString (lib.subtractLists buttonNames (builtins.attrNames cfg.buttons))}; "
          + "valid buttons: ${lib.concatStringsSep ", " buttonNames}";
      }
    ];

    # enable the daemon + its logiretch source behind the scenes; mkDefault
    # so a host already running magicbus for the Edge/Moonlander wins.
    universe.home.magicbus.enable = lib.mkDefault true;
    universe.home.magicbus.modules.logiretch = true;
    universe.home.magicbus.logiretch = {
      dpi = cfg.dpi;
      thumbwheel = cfg.thumbwheelInvert;
      haptic = cfg.haptic;
      smartShift =
        if (cfg.smartShift.threshold == null && cfg.smartShift.mode == null && cfg.smartShift.torque == null) then
          null
        else
          {
            inherit (cfg.smartShift) mode threshold torque;
          };
      takeoverReset = cfg.resetOnTakeover;
      buttons = if cfg.buttons == { } then null else buttonsList;
    };
  };
}
