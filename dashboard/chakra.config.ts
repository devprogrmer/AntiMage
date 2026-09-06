import { extendTheme } from "@chakra-ui/react";
import { mode, type StyleFunctionProps } from "@chakra-ui/theme-tools";

// The theme uses CSS variables for the primary color palette so we can
// switch named palettes at runtime by toggling a class on documentElement.
// The variables below provide sensible defaults which match the previous
// primary color scale.
const sharedThemeConfig = {
	config: {
		initialColorMode: "dark",
		useSystemColorMode: false,
	},
	direction: "ltr" as const,
	shadows: { outline: "0 0 0 2px var(--chakra-colors-primary-200)" },
	fonts: {
		body: `Arad,Inter,-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,Oxygen,Ubuntu,Cantarell,Fira Sans,Droid Sans,Helvetica Neue,"Apple Color Emoji","Segoe UI Emoji","Segoe UI Symbol",sans-serif`,
	},
	colors: {
		"light-border": "#d2d2d4",
		panel: {
			app: "var(--am-panel-bg)",
			main: "var(--am-panel-main)",
			sidebar: "var(--am-panel-sidebar)",
			surface: "var(--am-panel-surface)",
			elevated: "var(--am-panel-elevated)",
			border: "var(--am-panel-border)",
			borderStrong: "var(--am-panel-border-strong)",
			text: "var(--am-panel-text)",
			textSecondary: "var(--am-panel-text-secondary)",
			textMuted: "var(--am-panel-text-muted)",
			accent: "var(--am-panel-accent)",
			accentHover: "var(--am-panel-accent-hover)",
			warning: "#f59e0b",
			success: "#22c55e",
			danger: "#ef4444",
		},
		bg: {
			light: "var(--bg-light)",
			dark: "var(--bg-dark)",
		},
		surface: {
			light: "var(--surface-light)",
			dark: "var(--surface-dark)",
		},
		// primary color scale reads from CSS variables so swapping theme is just
		// adding/removing a class that sets a different set of --primary-* vars.
		primary: {
			50: "var(--primary-50)",
			100: "var(--primary-100)",
			200: "var(--primary-200)",
			300: "var(--primary-300)",
			400: "var(--primary-400)",
			500: "var(--primary-500)",
			600: "var(--primary-600)",
			700: "var(--primary-700)",
			800: "var(--primary-800)",
			900: "var(--primary-900)",
		},
		gray: {
			750: "#222C3B",
		},
	},
	// global styles: panel tokens and primary colors are CSS variables so
	// theme/accent can switch at runtime without remounting the app.
	styles: {
		global: {
			".chakra-modal__overlay": {
				bg: "blackAlpha.500 !important",
				backdropFilter: "none !important",
				WebkitBackdropFilter: "none !important",
			},
			".chakra-modal__content": {
				backgroundColor: "var(--am-panel-surface) !important",
				color: "var(--am-panel-text) !important",
				borderColor: "var(--am-panel-border) !important",
				borderRadius: "16px !important",
				boxShadow: "0 24px 72px rgba(0, 0, 0, 0.46) !important",
			},
			":root": {
				"--primary-50": "#fff0f0",
				"--primary-100": "#ffd5d6",
				"--primary-200": "#ffb0b2",
				"--primary-300": "#ff8c8f",
				"--primary-400": "#f9767b",
				"--primary-500": "#f05e63",
				"--primary-600": "#d84852",
				"--primary-700": "#b83846",
				"--primary-800": "#8f2d3a",
				"--primary-900": "#64232e",
				"--bg-light": "#0b1118",
				"--bg-dark": "#0b1118",
				"--surface-light": "#17232d",
				"--surface-dark": "#17232d",
			},

			".am-theme-dark": {
				"--am-panel-bg": "#0b1118",
				"--am-panel-main": "#0e151d",
				"--am-panel-sidebar": "#121d26",
				"--am-panel-surface": "#17232d",
				"--am-panel-elevated": "#1d2c37",
				"--am-panel-border": "#2b3a45",
				"--am-panel-border-strong": "#3c505d",
				"--am-panel-text": "#eef5f4",
				"--am-panel-text-secondary": "#c2d0d1",
				"--am-panel-text-muted": "#81939a",
				"--bg-light": "#0b1118",
				"--bg-dark": "#0b1118",
				"--surface-light": "#17232d",
				"--surface-dark": "#17232d",
			},
			".am-theme-light": {
				"--am-panel-bg": "#f4f5f7",
				"--am-panel-main": "#f7f8fa",
				"--am-panel-sidebar": "#ffffff",
				"--am-panel-surface": "#ffffff",
				"--am-panel-elevated": "#eef0f3",
				"--am-panel-border": "#d8dce2",
				"--am-panel-border-strong": "#c2c8d0",
				"--am-panel-text": "#17191c",
				"--am-panel-text-secondary": "#4f5661",
				"--am-panel-text-muted": "#7a828e",
				"--bg-light": "#f4f5f7",
				"--bg-dark": "#f4f5f7",
				"--surface-light": "#ffffff",
				"--surface-dark": "#ffffff",
			},
			body: {
				backgroundColor: "panel.main",
				color: "panel.text",
			},
			"[data-theme='dark'] body, .chakra-ui-dark body": {
				backgroundColor: "panel.main",
				color: "panel.text",
			},

			".am-seasonal-christmas": {
				"--primary-50": "#ffe6e6",
				"--primary-100": "#ffcdd2",
				"--primary-200": "#ef9a9a",
				"--primary-300": "#e57373",
				"--primary-400": "#ef5350",
				"--primary-500": "#d32f2f",
				"--primary-600": "#c62828",
				"--primary-700": "#b71c1c",
				"--primary-800": "#8d0f0f",
				"--primary-900": "#5f0a0a",
				"--bg-light": "#fdf7f2",
				"--bg-dark": "#0b0f19",
				"--surface-light": "#f7eee8",
				"--surface-dark": "#172235",
			},
		},
	},
	components: {
		Card: {
			baseStyle: (props: StyleFunctionProps) => ({
				container: {
					bg: mode("panel.surface", "panel.surface")(props),
					borderWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
					boxShadow: "none",
					borderRadius: "6px",
				},
			}),
		},
		Modal: {
			baseStyle: (props: StyleFunctionProps) => ({
				dialog: {
					bg: mode("panel.surface", "panel.surface")(props),
					borderWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
					borderRadius: "6px",
					boxShadow: "0 20px 60px rgba(0, 0, 0, 0.42)",
				},
				header: {
					borderBottomWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
				},
				footer: {
					borderTopWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
				},
			}),
		},
		Drawer: {
			baseStyle: (props: StyleFunctionProps) => ({
				dialog: {
					bg: mode("panel.surface", "panel.surface")(props),
					borderColor: mode("panel.border", "panel.border")(props),
					borderWidth: "0",
				},
			}),
		},
		Menu: {
			baseStyle: (props: StyleFunctionProps) => {
				const hoveamg = mode("panel.elevated", "panel.elevated")(props);
				return {
					list: {
						bg: mode("panel.surface", "panel.surface")(props),
						borderWidth: "1px",
						borderColor: mode("panel.border", "panel.border")(props),
						boxShadow: "0 18px 48px rgba(0, 0, 0, 0.38)",
					},
					item: {
						bg: "transparent !important",
						color: mode("panel.text", "panel.text")(props),
						_hover: {
							bg: `${hoveamg} !important`,
						},
						_focus: {
							bg: `${hoveamg} !important`,
						},
						_active: {
							bg: `${hoveamg} !important`,
						},
					},
				};
			},
		},
		Popover: {
			baseStyle: (props: StyleFunctionProps) => ({
				content: {
					bg: mode("panel.surface", "panel.surface")(props),
					borderWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
					boxShadow: "0 18px 48px rgba(0, 0, 0, 0.38)",
				},
				header: {
					borderBottomWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
				},
				footer: {
					borderTopWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
				},
			}),
		},
		Accordion: {
			baseStyle: (props: StyleFunctionProps) => ({
				container: {
					borderTopWidth: "0",
					borderBottomWidth: "1px",
					borderColor: mode("panel.border", "panel.border")(props),
					_last: {
						borderBottomWidth: "1px",
					},
				},
				button: {
					bg: "transparent",
					_hover: {
						bg: mode("panel.elevated", "panel.elevated")(props),
					},
					_expanded: {
						bg: mode("panel.elevated", "panel.elevated")(props),
					},
				},
				panel: {
					bg: mode("panel.surface", "panel.surface")(props),
				},
			}),
		},
		Alert: {
			baseStyle: {
				container: {
					borderRadius: "6px",
					fontSize: "sm",
				},
			},
		},
		Select: {
			baseStyle: {
				field: {
					bg: "panel.surface",
					color: "panel.text",
					_dark: {
						borderColor: "panel.borderStrong",
						borderRadius: "6px",
					},
					_light: {
						borderRadius: "6px",
					},
				},
			},
		},
		FormHelperText: {
			baseStyle: {
				fontSize: "xs",
			},
		},
		FormLabel: {
			baseStyle: {
				fontSize: "sm",
				fontWeight: "medium",
				mb: "1",
				_dark: { color: "panel.textSecondary" },
			},
		},
		Input: {
			baseStyle: {
				addon: {
					bg: "panel.elevated",
					_dark: {
						borderColor: "panel.borderStrong",
						_placeholder: {
							color: "panel.textMuted",
						},
					},
				},
				field: {
					bg: "panel.surface",
					color: "panel.text",
					_focusVisible: {
						boxShadow: "none",
						borderColor: "primary.500",
						outlineColor: "primary.500",
					},
					_dark: {
						borderColor: "panel.borderStrong",
						_disabled: {
							color: "panel.textMuted",
							borderColor: "panel.border",
						},
						_placeholder: {
							color: "panel.textMuted",
						},
					},
				},
			},
		},
		Table: {
			baseStyle: {
				table: {
					borderCollapse: "separate",
					borderSpacing: 0,
				},
				thead: {
					borderBottomColor: "light-border",
				},
				th: {
					background: "panel.elevated",
					color: "panel.text",
					borderColor: "panel.border !important",
					borderBottomColor: "panel.border !important",
					borderTop: "1px solid ",
					borderTopColor: "panel.border !important",
					_first: {
						borderLeft: "1px solid",
						borderColor: "panel.border !important",
					},
					_last: {
						borderRight: "1px solid",
						borderColor: "panel.border !important",
					},
					_dark: {
						borderColor: "panel.border !important",
						background: "panel.elevated",
					},
				},
				td: {
					transition: "all .1s ease-out",
					borderColor: "panel.border",
					borderBottomColor: "panel.border !important",
					_first: {
						borderLeft: "1px solid",
						borderColor: "panel.border",
						_dark: {
							borderColor: "panel.border",
						},
					},
					_last: {
						borderRight: "1px solid",
						borderColor: "panel.border",
						_dark: {
							borderColor: "panel.border",
						},
					},
					_dark: {
						borderColor: "panel.border",
						borderBottomColor: "panel.border !important",
					},
				},
				tr: {
					"&.interactive": {
						cursor: "pointer",
						_hover: {
							"& > td": {
								bg: "panel.elevated",
							},
							_dark: {
								"& > td": {
									bg: "panel.elevated",
								},
							},
						},
					},
					_last: {
						"& > td": {
							_first: {
								borderBottomLeftRadius: "8px",
							},
							_last: {
								borderBottomRightRadius: "8px",
							},
						},
					},
				},
			},
		},
		Button: {
			variants: {
				outline: (props: StyleFunctionProps) => ({
					borderColor: mode("blackAlpha.300", "whiteAlpha.300")(props),
					_hover: {
						bg: mode("blackAlpha.50", "whiteAlpha.100")(props),
					},
					_active: {
						bg: mode("blackAlpha.100", "whiteAlpha.200")(props),
					},
				}),
			},
		},
	},
};

export const theme = extendTheme(sharedThemeConfig);
export const rtlTheme = extendTheme({ ...sharedThemeConfig, direction: "rtl" });
