// Win32 bits mw4w.ps1 needs. Compiled once to cache\mw4w-native.dll.
using System;
using System.Collections.Generic;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;

namespace Mw4w
{
    public static class Native
    {
        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        static extern bool SystemParametersInfo(int action, int param, string value, int flags);

        [DllImport("user32.dll")]
        static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);

        [DllImport("user32.dll")]
        static extern IntPtr GetForegroundWindow();

        [DllImport("powrprof.dll")]
        static extern bool SetSuspendState(bool hibernate, bool force, bool disableWake);

        [DllImport("shell32.dll")]
        static extern IntPtr SHAppBarMessage(uint msg, ref APPBARDATA data);

        [StructLayout(LayoutKind.Sequential)]
        struct APPBARDATA
        {
            public int cbSize; public IntPtr hWnd; public uint uCallbackMessage; public uint uEdge;
            public int left, top, right, bottom; public IntPtr lParam;
        }

        public static void SetWallpaper(string path)
        {
            // SPI_SETDESKWALLPAPER, SPIF_UPDATEINIFILE | SPIF_SENDCHANGE
            SystemParametersInfo(0x14, 0, path, 0x3);
        }

        public static uint PidOf(long hwnd)
        {
            uint pid;
            GetWindowThreadProcessId(hwnd == 0 ? GetForegroundWindow() : new IntPtr(hwnd), out pid);
            return pid;
        }

        public static void Suspend() { SetSuspendState(false, false, false); }

        // Power mode (Settings > System > Power): an overlay on the plan. Exported by powrprof
        // since Windows 10 1709 but undocumented, hence the guards.
        [DllImport("powrprof.dll")]
        static extern uint PowerGetEffectiveOverlayScheme(out Guid overlay);

        [DllImport("powrprof.dll")]
        static extern uint PowerSetActiveOverlayScheme(Guid overlay);

        static readonly string[] modes = { "961cc777-2547-4f9d-8174-7d86181b8a7a", "00000000-0000-0000-0000-000000000000", "ded574b5-45a0-4f42-8737-46345c09c238" };

        // 0 = best efficiency, 1 = balanced, 2 = best performance, -1 = unknown
        public static int PowerMode()
        {
            try
            {
                Guid g;
                if (PowerGetEffectiveOverlayScheme(out g) != 0) return -1;
                return Array.IndexOf(modes, g.ToString());
            }
            catch (EntryPointNotFoundException) { return -1; }
        }

        public static void SetPowerMode(int mode)
        {
            try { PowerSetActiveOverlayScheme(new Guid(modes[Math.Max(0, Math.Min(2, mode))])); }
            catch (EntryPointNotFoundException) { }
        }

        public static void TaskbarAutoHide(bool on)
        {
            var d = new APPBARDATA();
            d.cbSize = Marshal.SizeOf(d);
            d.lParam = new IntPtr(on ? 1 : 2); // ABS_AUTOHIDE : ABS_ALWAYSONTOP
            SHAppBarMessage(0xA, ref d); // ABM_SETSTATE
        }

        public static bool TaskbarIsAutoHide()
        {
            var d = new APPBARDATA();
            d.cbSize = Marshal.SizeOf(d);
            return ((long)SHAppBarMessage(0x4, ref d) & 1) == 1; // ABM_GETSTATE
        }

        // ---------- thumbnails ----------
        public static void Thumb(string src, string dst, int w, int h)
        {
            using (var img = Image.FromFile(src))
            using (var bmp = new Bitmap(w, h))
            using (var g = Graphics.FromImage(bmp))
            {
                g.InterpolationMode = InterpolationMode.HighQualityBicubic;
                double s = Math.Max((double)w / img.Width, (double)h / img.Height);
                int dw = (int)(img.Width * s), dh = (int)(img.Height * s);
                g.DrawImage(img, (w - dw) / 2, (h - dh) / 2, dw, dh);
                bmp.Save(dst, ImageFormat.Jpeg);
            }
        }

        // ---------- dominant hue (pywal / matugen-ish) ----------
        // Returns "h,s,l" of the most prominent saturated colour, weighted by
        // saturation*value so muddy pixels don't win. Grey images give s=0.
        public static string Dominant(string path)
        {
            var bins = new double[36];
            var sat = new double[36];
            double lsum = 0; int n = 0;
            using (var img = Image.FromFile(path))
            using (var bmp = new Bitmap(img, 64, 36))
            {
                for (int y = 0; y < bmp.Height; y++)
                    for (int x = 0; x < bmp.Width; x++)
                    {
                        var c = bmp.GetPixel(x, y);
                        float hue = c.GetHue(), s = c.GetSaturation(), l = c.GetBrightness();
                        lsum += l; n++;
                        if (s < 0.2 || l < 0.12 || l > 0.92) continue;
                        double wgt = s * (1 - Math.Abs(l - 0.5) * 1.4);
                        int b = ((int)(hue / 10)) % 36;
                        bins[b] += wgt; sat[b] += s * wgt;
                    }
            }
            int best = 0;
            for (int i = 0; i < 36; i++)
            {
                // smooth over neighbours so a hue split between two bins still wins
                double a = bins[i] + 0.5 * (bins[(i + 35) % 36] + bins[(i + 1) % 36]);
                double bb = bins[best] + 0.5 * (bins[(best + 35) % 36] + bins[(best + 1) % 36]);
                if (a > bb) best = i;
            }
            double total = 0; foreach (var v in bins) total += v;
            if (total < 1) return "220,0," + (lsum / n).ToString("0.00", System.Globalization.CultureInfo.InvariantCulture);
            double avgSat = sat[best] / Math.Max(bins[best], 1e-9);
            return string.Format(System.Globalization.CultureInfo.InvariantCulture, "{0},{1:0.00},{2:0.00}", best * 10 + 5, avgSat, lsum / n);
        }

        // ---------- app icons via the shell (works for Win32 + UWP) ----------
        [ComImport, Guid("bcc18b79-ba16-442f-80c4-8a59c30c463b"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
        interface IShellItemImageFactory
        {
            [PreserveSig] int GetImage(SIZE size, int flags, out IntPtr phbm);
        }

        [StructLayout(LayoutKind.Sequential)]
        struct SIZE { public int cx, cy; }

        [DllImport("shell32.dll", CharSet = CharSet.Unicode, PreserveSig = false)]
        static extern void SHCreateItemFromParsingName(string path, IntPtr pbc, [MarshalAs(UnmanagedType.LPStruct)] Guid riid, [MarshalAs(UnmanagedType.Interface)] out IShellItemImageFactory item);

        [DllImport("gdi32.dll")]
        static extern bool DeleteObject(IntPtr h);

        // Saves PNGs for many apps in one process start. Returns count saved.
        public static int Icons(string[] appIds, string[] outFiles, int size)
        {
            int ok = 0;
            for (int i = 0; i < appIds.Length; i++)
            {
                try { if (Icon(appIds[i], outFiles[i], size)) ok++; } catch { }
            }
            return ok;
        }

        static bool Icon(string appId, string outFile, int size)
        {
            IShellItemImageFactory f;
            // App IDs from Get-StartApps, or a plain file path (exe of an open window).
            SHCreateItemFromParsingName(appId.Contains(":\\") ? appId : "shell:AppsFolder\\" + appId, IntPtr.Zero, typeof(IShellItemImageFactory).GUID, out f);
            IntPtr hbm;
            // SIIGBF_BIGGERSIZEOK | SIIGBF_ICONONLY
            if (f.GetImage(new SIZE { cx = size, cy = size }, 0x1 | 0x4, out hbm) != 0) return false;
            try
            {
                using (var src = Image.FromHbitmap(hbm))
                using (var bmp = WithAlpha(hbm, src.Width, src.Height))
                    bmp.Save(outFile, ImageFormat.Png);
                return true;
            }
            finally { DeleteObject(hbm); }
        }

        [DllImport("gdi32.dll")]
        static extern int GetObject(IntPtr h, int size, ref BITMAP bm);

        [StructLayout(LayoutKind.Sequential)]
        struct BITMAP { public int bmType, bmWidth, bmHeight, bmWidthBytes; public ushort bmPlanes, bmBitsPixel; public IntPtr bmBits; }

        // Image.FromHbitmap drops alpha; read the 32bpp DIB bits directly.
        static Bitmap WithAlpha(IntPtr hbm, int w, int h)
        {
            var bm = new BITMAP();
            GetObject(hbm, Marshal.SizeOf(bm), ref bm);
            if (bm.bmBitsPixel != 32 || bm.bmBits == IntPtr.Zero) return Image.FromHbitmap(hbm);
            var outBmp = new Bitmap(w, h, PixelFormat.Format32bppArgb);
            var data = outBmp.LockBits(new Rectangle(0, 0, w, h), ImageLockMode.WriteOnly, PixelFormat.Format32bppArgb);
            var row = new byte[bm.bmWidthBytes];
            for (int y = 0; y < h; y++)
            {
                // DIB sections from the shell are bottom-up.
                Marshal.Copy(new IntPtr(bm.bmBits.ToInt64() + (long)(h - 1 - y) * bm.bmWidthBytes), row, 0, row.Length);
                Marshal.Copy(row, 0, new IntPtr(data.Scan0.ToInt64() + (long)y * data.Stride), Math.Min(row.Length, data.Stride));
            }
            outBmp.UnlockBits(data);
            return outBmp;
        }
    }
    // Per-app volume for the media island: the Core Audio sessions of the default output
    // device (what the volume mixer shows), like MediaControll's PipeWire stream volume.
    public static class Audio
    {
        // Volume 0..100 of the first session whose process name is in procs (no .exe);
        // set >= 0 sets every matching session first. -1: the app has no audio session.
        public static int AppVolume(string[] procs, int set)
        {
            var en = (IMMDeviceEnumerator)new MMDeviceEnumerator();
            IMMDevice dev;
            if (en.GetDefaultAudioEndpoint(0, 1, out dev) != 0) return -1; // eRender, eMultimedia
            object o;
            var iid = typeof(IAudioSessionManager2).GUID;
            if (dev.Activate(ref iid, 0x17, IntPtr.Zero, out o) != 0) return -1; // CLSCTX_ALL
            IAudioSessionEnumerator list;
            if (((IAudioSessionManager2)o).GetSessionEnumerator(out list) != 0) return -1;
            int n;
            list.GetCount(out n);
            int result = -1;
            var ctx = Guid.Empty;
            for (int i = 0; i < n; i++)
            {
                IAudioSessionControl2 s;
                if (list.GetSession(i, out s) != 0) continue;
                uint pid;
                s.GetProcessId(out pid);
                if (pid == 0) continue; // system sounds
                string name;
                try { name = System.Diagnostics.Process.GetProcessById((int)pid).ProcessName; }
                catch { continue; }
                if (Array.FindIndex(procs, p => string.Equals(p, name, StringComparison.OrdinalIgnoreCase)) < 0) continue;
                var v = (ISimpleAudioVolume)s;
                if (set >= 0) v.SetMasterVolume(Math.Min(100, set) / 100f, ref ctx);
                float f;
                v.GetMasterVolume(out f);
                if (result < 0) result = (int)Math.Round(f * 100);
            }
            return result;
        }

        static IAudioSessionEnumerator SessionList()
        {
            var en = (IMMDeviceEnumerator)new MMDeviceEnumerator();
            IMMDevice dev;
            if (en.GetDefaultAudioEndpoint(0, 1, out dev) != 0) return null;
            object o;
            var iid = typeof(IAudioSessionManager2).GUID;
            if (dev.Activate(ref iid, 0x17, IntPtr.Zero, out o) != 0) return null;
            IAudioSessionEnumerator list;
            return ((IAudioSessionManager2)o).GetSessionEnumerator(out list) == 0 ? list : null;
        }

        // The volume mixer for the quick panel: one "process<TAB>volume<TAB>muted<TAB>exe path" per app
        // with a live session on the default output (an app with several sessions is listed once).
        public static string[] Mixer()
        {
            var rows = new System.Collections.Generic.List<string>();
            var seen = new System.Collections.Generic.HashSet<string>(StringComparer.OrdinalIgnoreCase);
            var list = SessionList();
            if (list == null) return rows.ToArray();
            int n;
            list.GetCount(out n);
            for (int i = 0; i < n; i++)
            {
                IAudioSessionControl2 s;
                if (list.GetSession(i, out s) != 0) continue;
                int state;
                uint pid;
                s.GetState(out state);
                s.GetProcessId(out pid);
                if (state == 2 || pid == 0) continue; // expired, system sounds
                string name, path = "";
                try
                {
                    var p = System.Diagnostics.Process.GetProcessById((int)pid);
                    name = p.ProcessName;
                    try { path = p.MainModule.FileName; } catch { }
                }
                catch { continue; }
                if (!seen.Add(name)) continue;
                var v = (ISimpleAudioVolume)s;
                float f;
                bool m;
                v.GetMasterVolume(out f);
                v.GetMute(out m);
                rows.Add(name + "\t" + (int)Math.Round(f * 100) + "\t" + (m ? 1 : 0) + "\t" + path);
            }
            return rows.ToArray();
        }

        // Sets every session of process `name`: volume 0..100 and/or mute 0/1 (-1 = leave as is).
        public static void SetApp(string name, int volume, int mute)
        {
            var list = SessionList();
            if (list == null) return;
            int n;
            list.GetCount(out n);
            var ctx = Guid.Empty;
            for (int i = 0; i < n; i++)
            {
                IAudioSessionControl2 s;
                if (list.GetSession(i, out s) != 0) continue;
                uint pid;
                s.GetProcessId(out pid);
                if (pid == 0) continue;
                try { if (!string.Equals(System.Diagnostics.Process.GetProcessById((int)pid).ProcessName, name, StringComparison.OrdinalIgnoreCase)) continue; }
                catch { continue; }
                var v = (ISimpleAudioVolume)s;
                if (volume >= 0) v.SetMasterVolume(Math.Min(100, volume) / 100f, ref ctx);
                if (mute >= 0) v.SetMute(mute == 1, ref ctx);
            }
        }

        // Default output/input device (an MMDevice id, as Zebar's deviceId) for all three roles,
        // with the undocumented IPolicyConfig that the Sound settings page uses.
        public static void SetDefault(string id)
        {
            var pc = (IPolicyConfig)new PolicyConfigClient();
            for (int role = 0; role < 3; role++) pc.SetDefaultEndpoint(id, role);
        }
    }

    [ComImport, Guid("870AF99C-171D-4F9E-AF0D-E63DF40C2BC9")]
    class PolicyConfigClient { }

    [ComImport, Guid("F8679F50-850A-41CF-9C72-430F290290C8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IPolicyConfig
    {
        [PreserveSig] int GetMixFormat(IntPtr a, IntPtr b);
        [PreserveSig] int GetDeviceFormat(IntPtr a, int b, IntPtr c);
        [PreserveSig] int ResetDeviceFormat(IntPtr a);
        [PreserveSig] int SetDeviceFormat(IntPtr a, IntPtr b, IntPtr c);
        [PreserveSig] int GetProcessingPeriod(IntPtr a, int b, IntPtr c, IntPtr d);
        [PreserveSig] int SetProcessingPeriod(IntPtr a, IntPtr b);
        [PreserveSig] int GetShareMode(IntPtr a, IntPtr b);
        [PreserveSig] int SetShareMode(IntPtr a, IntPtr b);
        [PreserveSig] int GetPropertyValue(IntPtr a, int b, IntPtr c, IntPtr d);
        [PreserveSig] int SetPropertyValue(IntPtr a, int b, IntPtr c, IntPtr d);
        [PreserveSig] int SetDefaultEndpoint([MarshalAs(UnmanagedType.LPWStr)] string id, int role);
        [PreserveSig] int SetEndpointVisibility([MarshalAs(UnmanagedType.LPWStr)] string id, int visible);
    }

    [ComImport, Guid("BCDE0395-E52F-467C-8E3D-C4579291692E")]
    class MMDeviceEnumerator { }

    [ComImport, Guid("A95664D2-9614-4F35-A746-DE8DB63617E6"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IMMDeviceEnumerator
    {
        [PreserveSig] int EnumAudioEndpoints(int dataFlow, int stateMask, out IntPtr devices);
        [PreserveSig] int GetDefaultAudioEndpoint(int dataFlow, int role, out IMMDevice device);
    }

    [ComImport, Guid("D666063F-1587-4E43-81F1-B948E807363F"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IMMDevice
    {
        [PreserveSig] int Activate(ref Guid iid, int clsCtx, IntPtr activationParams, [MarshalAs(UnmanagedType.IUnknown)] out object iface);
    }

    // vtable order matters: IAudioSessionManager's two methods come first
    [ComImport, Guid("77AA99A0-1BD6-484F-8BC7-2C654C9A9B6F"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IAudioSessionManager2
    {
        [PreserveSig] int GetAudioSessionControl(IntPtr sessionGuid, int flags, out IntPtr control);
        [PreserveSig] int GetSimpleAudioVolume(IntPtr sessionGuid, int flags, out IntPtr volume);
        [PreserveSig] int GetSessionEnumerator(out IAudioSessionEnumerator list);
    }

    [ComImport, Guid("E2F5BB11-0570-40CA-ACDD-3AA01277DEE8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IAudioSessionEnumerator
    {
        [PreserveSig] int GetCount(out int count);
        [PreserveSig] int GetSession(int index, out IAudioSessionControl2 session);
    }

    [ComImport, Guid("BFB7FF88-7239-4FC9-8FA2-07C950BE9C6D"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IAudioSessionControl2
    {
        // IAudioSessionControl
        [PreserveSig] int GetState(out int state);
        [PreserveSig] int GetDisplayName(out IntPtr name);
        [PreserveSig] int SetDisplayName(IntPtr name, IntPtr ctx);
        [PreserveSig] int GetIconPath(out IntPtr path);
        [PreserveSig] int SetIconPath(IntPtr path, IntPtr ctx);
        [PreserveSig] int GetGroupingParam(out Guid param);
        [PreserveSig] int SetGroupingParam(IntPtr param, IntPtr ctx);
        [PreserveSig] int RegisterAudioSessionNotification(IntPtr client);
        [PreserveSig] int UnregisterAudioSessionNotification(IntPtr client);
        // IAudioSessionControl2
        [PreserveSig] int GetSessionIdentifier(out IntPtr id);
        [PreserveSig] int GetSessionInstanceIdentifier(out IntPtr id);
        [PreserveSig] int GetProcessId(out uint pid);
    }

    [ComImport, Guid("87CE5498-68D6-44E5-9215-6DA47EF883D8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface ISimpleAudioVolume
    {
        [PreserveSig] int SetMasterVolume(float level, ref Guid ctx);
        [PreserveSig] int GetMasterVolume(out float level);
        [PreserveSig] int SetMute(bool mute, ref Guid ctx);
        [PreserveSig] int GetMute(out bool mute);
    }

    // One active display. Mode is "WxH@Hz" unrotated; X/Y/Width/Height are its desktop rect.
    public class Display
    {
        public string Device { get; set; }
        public string Name { get; set; }
        public bool Primary { get; set; }
        public int X { get; set; }
        public int Y { get; set; }
        public int Width { get; set; }
        public int Height { get; set; }
        public string Mode { get; set; }
        public int Orientation { get; set; }
        public int Scale { get; set; }
        public int[] Scales { get; set; }
        public string[] Modes { get; set; }
    }

    // Displays: modes and layout through GDI, names and scale through DisplayConfig. The scale uses
    // DISPLAYCONFIG_DEVICE_INFO_GET/SET_DPI_SCALE (-3/-4): undocumented, what Settings > Display uses.
    public static class Displays
    {
        static readonly int[] ScaleSteps = { 100, 125, 150, 175, 200, 225, 250, 300, 350, 400, 450, 500 };

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        struct DEVMODE
        {
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string dmDeviceName;
            public short dmSpecVersion, dmDriverVersion, dmSize, dmDriverExtra;
            public int dmFields, dmPositionX, dmPositionY, dmDisplayOrientation, dmDisplayFixedOutput;
            public short dmColor, dmDuplex, dmYResolution, dmTTOption, dmCollate;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string dmFormName;
            public short dmLogPixels;
            public int dmBitsPerPel, dmPelsWidth, dmPelsHeight, dmDisplayFlags, dmDisplayFrequency,
                dmICMMethod, dmICMIntent, dmMediaType, dmDitherType, dmReserved1, dmReserved2, dmPanningWidth, dmPanningHeight;
        }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        struct DISPLAY_DEVICE
        {
            public int cb;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string DeviceName;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string DeviceString;
            public int StateFlags;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string DeviceID;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string DeviceKey;
        }

        [StructLayout(LayoutKind.Sequential)] struct LUID { public uint Low; public int High; }

        [StructLayout(LayoutKind.Sequential)]
        struct PATH_INFO
        {
            public LUID srcAdapter; public uint srcId, srcModeIdx, srcFlags;
            public LUID tgtAdapter; public uint tgtId, tgtModeIdx; public int tech, rotation, scaling;
            public uint rateN, rateD; public int scanline, available; public uint tgtFlags;
            public uint flags;
        }

        [StructLayout(LayoutKind.Sequential, Size = 64)] struct MODE_INFO { public int type; }
        [StructLayout(LayoutKind.Sequential)] struct HEADER { public int type; public int size; public LUID adapter; public uint id; }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        struct SOURCE_NAME { public HEADER h; [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string gdi; }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        struct TARGET_NAME
        {
            public HEADER h; public uint flags; public int tech; public ushort edidMan, edidProd; public uint connector;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string friendly;
            [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string path;
        }

        [StructLayout(LayoutKind.Sequential)] struct DPI_GET { public HEADER h; public int min, cur, max; }
        [StructLayout(LayoutKind.Sequential)] struct DPI_SET { public HEADER h; public int rel; }

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        static extern bool EnumDisplayDevices(string device, uint i, ref DISPLAY_DEVICE dd, uint flags);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        static extern bool EnumDisplaySettingsEx(string device, int mode, ref DEVMODE dm, int flags);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        static extern int ChangeDisplaySettingsEx(string device, ref DEVMODE dm, IntPtr hwnd, int flags, IntPtr param);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        static extern int ChangeDisplaySettingsEx(string device, IntPtr dm, IntPtr hwnd, int flags, IntPtr param);
        [DllImport("user32.dll")] static extern int GetDisplayConfigBufferSizes(uint flags, out uint paths, out uint modes);
        [DllImport("user32.dll")]
        static extern int QueryDisplayConfig(uint flags, ref uint np, [Out] PATH_INFO[] paths, ref uint nm, [Out] MODE_INFO[] modes, IntPtr topology);
        [DllImport("user32.dll")] static extern int DisplayConfigGetDeviceInfo(ref SOURCE_NAME r);
        [DllImport("user32.dll")] static extern int DisplayConfigGetDeviceInfo(ref TARGET_NAME r);
        [DllImport("user32.dll")] static extern int DisplayConfigGetDeviceInfo(ref DPI_GET r);
        [DllImport("user32.dll")] static extern int DisplayConfigSetDeviceInfo(ref DPI_SET r);

        static DEVMODE NewMode() { var m = new DEVMODE(); m.dmSize = (short)Marshal.SizeOf(typeof(DEVMODE)); return m; }

        static HEADER Header(int type, Type t, LUID adapter, uint id)
        {
            var h = new HEADER(); h.type = type; h.size = Marshal.SizeOf(t); h.adapter = adapter; h.id = id; return h;
        }

        // the active path showing GDI device `device` (\\.\DISPLAY1)
        static bool PathOf(string device, out PATH_INFO path)
        {
            path = new PATH_INFO();
            uint np, nm;
            if (GetDisplayConfigBufferSizes(2, out np, out nm) != 0) return false; // QDC_ONLY_ACTIVE_PATHS
            var paths = new PATH_INFO[np];
            var modes = new MODE_INFO[nm];
            if (QueryDisplayConfig(2, ref np, paths, ref nm, modes, IntPtr.Zero) != 0) return false;
            for (int i = 0; i < np; i++)
            {
                var s = new SOURCE_NAME();
                s.h = Header(1, typeof(SOURCE_NAME), paths[i].srcAdapter, paths[i].srcId);
                if (DisplayConfigGetDeviceInfo(ref s) == 0 && s.gdi == device) { path = paths[i]; return true; }
            }
            return false;
        }

        // recommended scale index, current index, highest index
        static int[] DpiOf(PATH_INFO p)
        {
            var d = new DPI_GET();
            d.h = Header(-3, typeof(DPI_GET), p.srcAdapter, p.srcId);
            if (DisplayConfigGetDeviceInfo(ref d) != 0) return null;
            int rec = -d.min;
            return new[] { rec, Math.Min(rec + d.cur, ScaleSteps.Length - 1), Math.Min(rec + d.max, ScaleSteps.Length - 1) };
        }

        public static Display[] List()
        {
            var list = new List<Display>();
            var dd = new DISPLAY_DEVICE();
            dd.cb = Marshal.SizeOf(dd);
            for (uint i = 0; EnumDisplayDevices(null, i, ref dd, 0); i++, dd.cb = Marshal.SizeOf(dd))
            {
                if ((dd.StateFlags & 1) == 0) continue; // not DISPLAY_DEVICE_ATTACHED_TO_DESKTOP
                var cur = NewMode();
                if (!EnumDisplaySettingsEx(dd.DeviceName, -1, ref cur, 0)) continue; // ENUM_CURRENT_SETTINGS
                bool turned = (cur.dmDisplayOrientation & 1) != 0;
                var d = new Display
                {
                    Device = dd.DeviceName, Name = "", Primary = (dd.StateFlags & 4) != 0,
                    X = cur.dmPositionX, Y = cur.dmPositionY, Width = cur.dmPelsWidth, Height = cur.dmPelsHeight,
                    Orientation = cur.dmDisplayOrientation, Scale = 100, Scales = new[] { 100 },
                    Mode = (turned ? cur.dmPelsHeight : cur.dmPelsWidth) + "x" + (turned ? cur.dmPelsWidth : cur.dmPelsHeight) + "@" + cur.dmDisplayFrequency,
                };
                var all = new List<int[]>();
                var seen = new HashSet<string>();
                var m = NewMode();
                for (int k = 0; EnumDisplaySettingsEx(dd.DeviceName, k, ref m, 0); k++)
                {
                    if (m.dmBitsPerPel != 32) continue;
                    int w = m.dmPelsWidth, h = m.dmPelsHeight;
                    if ((m.dmDisplayOrientation & 1) != 0) { w = m.dmPelsHeight; h = m.dmPelsWidth; }
                    if (seen.Add(w + "x" + h + "@" + m.dmDisplayFrequency)) all.Add(new[] { w, h, m.dmDisplayFrequency });
                }
                all.Sort((a, b) => a[0] * a[1] != b[0] * b[1] ? b[0] * b[1] - a[0] * a[1] : b[2] - a[2]);
                d.Modes = all.ConvertAll(a => a[0] + "x" + a[1] + "@" + a[2]).ToArray();
                PATH_INFO p;
                if (PathOf(dd.DeviceName, out p))
                {
                    var t = new TARGET_NAME();
                    t.h = Header(2, typeof(TARGET_NAME), p.tgtAdapter, p.tgtId);
                    if (DisplayConfigGetDeviceInfo(ref t) == 0) d.Name = t.friendly;
                    if (d.Name == "" && p.tech == unchecked((int)0x80000000)) d.Name = "Built-in display"; // OUTPUT_TECHNOLOGY_INTERNAL
                    var dpi = DpiOf(p);
                    if (dpi != null)
                    {
                        d.Scale = ScaleSteps[dpi[1]];
                        var sc = new int[dpi[2] + 1];
                        Array.Copy(ScaleSteps, sc, sc.Length);
                        d.Scales = sc;
                    }
                }
                if (d.Name == "") d.Name = dd.DeviceString;
                list.Add(d);
            }
            return list.ToArray();
        }

        // Stages a mode (width/height unrotated) for Commit. 0 = DISP_CHANGE_SUCCESSFUL.
        public static int Stage(string device, int w, int h, int hz, int x, int y, int orientation, bool primary)
        {
            var dm = NewMode();
            EnumDisplaySettingsEx(device, -1, ref dm, 0);
            bool turned = (orientation & 1) != 0;
            dm.dmPelsWidth = turned ? h : w;
            dm.dmPelsHeight = turned ? w : h;
            dm.dmDisplayFrequency = hz;
            dm.dmPositionX = x;
            dm.dmPositionY = y;
            dm.dmDisplayOrientation = orientation;
            dm.dmFields = 0x20 | 0x80 | 0x80000 | 0x100000 | 0x400000; // position, orientation, width, height, frequency
            // CDS_UPDATEREGISTRY | CDS_NORESET (| CDS_SET_PRIMARY)
            return ChangeDisplaySettingsEx(device, ref dm, IntPtr.Zero, 0x10000000 | 1 | (primary ? 0x10 : 0), IntPtr.Zero);
        }

        public static int Commit() { return ChangeDisplaySettingsEx(null, IntPtr.Zero, IntPtr.Zero, 0, IntPtr.Zero); }

        public static int SetScale(string device, int percent)
        {
            PATH_INFO p;
            int idx = Array.IndexOf(ScaleSteps, percent);
            if (idx < 0 || !PathOf(device, out p)) return -1;
            var dpi = DpiOf(p);
            if (dpi == null) return -1;
            var s = new DPI_SET();
            s.h = Header(-4, typeof(DPI_SET), p.srcAdapter, p.srcId);
            s.rel = idx - dpi[0];
            return DisplayConfigSetDeviceInfo(ref s);
        }
    }
}
