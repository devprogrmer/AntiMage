import {
	Alert,
	AlertDescription,
	AlertIcon,
	Box,
	Button,
	chakra,
	FormControl,
	FormErrorMessage,
	FormLabel,
	HStack,
	IconButton,
	Input as CInput,
	InputGroup,
	InputLeftElement,
	InputRightElement,
	Menu,
	MenuButton,
	MenuItem,
	MenuList,
	Portal,
	Text,
	useColorMode,
	useColorModeValue,
	VStack,
} from "@chakra-ui/react";
import {
	ArrowRightOnRectangleIcon,
	CheckIcon,
	EyeIcon,
	EyeSlashIcon,
	LockClosedIcon,
	MoonIcon,
	SunIcon,
	UserIcon,
} from "@heroicons/react/24/outline";
import { zodResolver } from "@hookform/resolvers/zod";
import logoUrl from "assets/logo.svg";
import { Language } from "components/Language";
import {
	type FC,
	type ReactElement,
	type ReactNode,
	useEffect,
	useState,
} from "react";
import {
	type FieldErrors,
	useForm,
	type UseFormRegisterReturn,
} from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { QRCodeCanvas } from "qrcode.react";
import {
	confirm2FASetup,
	getSession,
	login as createSession,
	logout,
	start2FASetup,
	type TOTPSetup,
	verify2FA,
} from "service/auth";
import { clearClientSession } from "utils/session";
import { updateThemeColor } from "utils/themeColor";
import { z } from "zod";

const schema = z.object({
	username: z.string().min(1, "login.fieldRequired"),
	password: z.string().min(1, "login.fieldRequired"),
});

export const LogoIcon = chakra("img", {
	baseStyle: {
		h: 8,
		w: 8,
	},
});

const LoginIcon = chakra(ArrowRightOnRectangleIcon, {
	baseStyle: {
		h: 5,
		strokeWidth: "2px",
		w: 5,
	},
});

const Eye = chakra(EyeIcon, { baseStyle: { h: 4, w: 4 } });
const EyeSlash = chakra(EyeSlashIcon, { baseStyle: { h: 4, w: 4 } });
const User = chakra(UserIcon, { baseStyle: { h: 5, strokeWidth: "1.8px", w: 5 } });
const Lock = chakra(LockClosedIcon, {
	baseStyle: { h: 5, strokeWidth: "1.8px", w: 5 },
});
const Moon = chakra(MoonIcon, { baseStyle: { h: 4, w: 4 } });
const Sun = chakra(SunIcon, { baseStyle: { h: 4, w: 4 } });
const Check = chakra(CheckIcon, { baseStyle: { h: 4, w: 4 } });

const THEME_KEY = "am-theme";
const CHAKRA_THEME_KEY = "chakra-ui-color-mode";
const CUSTOM_THEMES_KEY = "am-custom-themes";

type LoginThemeMode = "dark" | "light";

type LoginFormValues = {
	username: string;
	password: string;
};

type LoginFieldProps = {
	autoComplete: string;
	dir: "ltr" | "rtl";
	endElement?: ReactNode;
	errorMessage?: string;
	icon: ReactNode;
	label: string;
	placeholder: string;
	registration: UseFormRegisterReturn;
	type?: string;
};

const LoginField: FC<LoginFieldProps> = ({
	autoComplete,
	dir,
	endElement,
	errorMessage,
	icon,
	label,
	placeholder,
	registration,
	type = "text",
}) => {
	const isInvalid = Boolean(errorMessage);
	const fieldBg = useColorModeValue("white", "var(--am-panel-main)");
	const borderColor = useColorModeValue(
		"var(--am-panel-border)",
		"var(--am-panel-border)",
	);
	const textColor = useColorModeValue(
		"var(--am-panel-text)",
		"var(--am-panel-text)",
	);
	const mutedColor = useColorModeValue(
		"var(--am-panel-text-muted)",
		"var(--am-panel-text-muted)",
	);

	return (
		<FormControl isInvalid={isInvalid}>
			<FormLabel
				color={textColor}
				fontSize="sm"
				fontWeight="700"
				letterSpacing="0"
				mb={2}
			>
				{label}
			</FormLabel>
			<InputGroup dir={dir}>
				<InputLeftElement color={isInvalid ? "red.400" : mutedColor} h="44px">
					{icon}
				</InputLeftElement>
				<CInput
					{...registration}
					autoComplete={autoComplete}
					bg={fieldBg}
					borderColor={isInvalid ? "red.400" : borderColor}
					borderRadius="8px"
					color={textColor}
					fontSize="sm"
					h="44px"
					pe={endElement ? "3rem" : 4}
					placeholder={placeholder}
					ps="3rem"
					type={type}
					_placeholder={{ color: mutedColor }}
					_hover={{
						borderColor: isInvalid
							? "red.400"
							: "var(--am-panel-border-strong)",
					}}
					_focusVisible={{
						borderColor: isInvalid ? "red.400" : "var(--am-panel-accent)",
						boxShadow: isInvalid
							? "0 0 0 1px rgba(248, 113, 113, 0.6)"
							: "0 0 0 1px var(--am-panel-accent)",
					}}
				/>
				{endElement && (
					<InputRightElement color={mutedColor} h="44px">
						{endElement}
					</InputRightElement>
				)}
			</InputGroup>
			<FormErrorMessage fontSize="xs">{errorMessage}</FormErrorMessage>
		</FormControl>
	);
};

const applyLoginThemeMode = (theme: LoginThemeMode) => {
	try {
		localStorage.setItem(THEME_KEY, theme);
		localStorage.setItem(CHAKRA_THEME_KEY, theme);
		localStorage.removeItem(CUSTOM_THEMES_KEY);
	} catch {}

	const targets = [document.documentElement, document.body].filter(
		Boolean,
	) as HTMLElement[];
	targets.forEach((target) => {
		target.classList.remove(
			"am-theme-light",
			"am-theme-dark",
			"chakra-ui-light",
			"chakra-ui-dark",
		);
		target.classList.add(`am-theme-${theme}`, `chakra-ui-${theme}`);
		target.dataset.theme = theme;
		target.style.colorScheme = theme;
	});
	updateThemeColor(theme);
};

const LoginThemeMenu: FC = () => {
	const { t } = useTranslation();
	const { colorMode, setColorMode } = useColorMode();
	const activeTheme = colorMode === "light" ? "light" : "dark";
	const menuBg = useColorModeValue("panel.surface", "panel.surface");
	const menuBorder = useColorModeValue("panel.border", "panel.border");
	const hoveamg = useColorModeValue("panel.elevated", "panel.elevated");
	const textColor = useColorModeValue("panel.text", "panel.text");

	const selectTheme = (theme: LoginThemeMode) => {
		applyLoginThemeMode(theme);
		setColorMode(theme);
	};

	const options: Array<{
		key: LoginThemeMode;
		label: string;
		icon: ReactElement;
	}> = [
		{ key: "dark", label: t("theme.dark"), icon: <Moon /> },
		{ key: "light", label: t("theme.light"), icon: <Sun /> },
	];

	return (
		<Menu placement="bottom-end" strategy="fixed" autoSelect={false}>
			<MenuButton
				as={IconButton}
				aria-label={t("header.theme")}
				icon={activeTheme === "dark" ? <Moon /> : <Sun />}
				size="sm"
				variant="ghost"
			/>
			<Portal>
				<MenuList
					bg={menuBg}
					borderColor={menuBorder}
					color={textColor}
					minW="150px"
					p={1}
				>
					{options.map((option) => (
						<MenuItem
							key={option.key}
							icon={option.icon}
							onClick={() => selectTheme(option.key)}
							_hover={{ bg: hoveamg }}
						>
							<HStack justify="space-between" w="full">
								<Text>{option.label}</Text>
								{activeTheme === option.key ? <Check /> : null}
							</HStack>
						</MenuItem>
					))}
				</MenuList>
			</Portal>
		</Menu>
	);
};

export const Login: FC = () => {
	const [error, setError] = useState("");
	const [showPassword, setShowPassword] = useState(false);
	const [step, setStep] = useState<"credentials" | "otp" | "setup">(
		"credentials",
	);
	const [otp, setOTP] = useState("");
	const [setup, setSetup] = useState<TOTPSetup | null>(null);
	const [challengeLoading, setChallengeLoading] = useState(false);
	const navigate = useNavigate();
	const { t, i18n } = useTranslation();
	const dir = i18n.language === "fa" ? "rtl" : "ltr";
	const pageBg = useColorModeValue(
		"var(--am-panel-main)",
		"var(--am-panel-main)",
	);
	const surfaceBg = useColorModeValue(
		"var(--am-panel-surface)",
		"var(--am-panel-surface)",
	);
	const elevatedBg = useColorModeValue(
		"var(--am-panel-elevated)",
		"var(--am-panel-elevated)",
	);
	const borderColor = useColorModeValue(
		"var(--am-panel-border)",
		"var(--am-panel-border)",
	);
	const textColor = useColorModeValue(
		"var(--am-panel-text)",
		"var(--am-panel-text)",
	);
	const mutedColor = useColorModeValue(
		"var(--am-panel-text-muted)",
		"var(--am-panel-text-muted)",
	);
	const accentColor = "var(--am-panel-accent)";
	const signalColor = "#63D8CF";

	const {
		register,
		formState: { errors, isSubmitting },
		handleSubmit,
		watch,
	} = useForm<LoginFormValues>({
		resolver: zodResolver(schema),
		defaultValues: {
			password: "",
			username: "",
		},
	});

	const usernameValue = watch("username") || "";
	const passwordValue = watch("password") || "";
	const canSubmit =
		Boolean(usernameValue.trim().length) &&
		Boolean(passwordValue.trim().length) &&
		!isSubmitting;

	useEffect(() => {
		void usernameValue;
		void passwordValue;
		setError((current) => (current ? "" : current));
	}, [usernameValue, passwordValue]);

	useEffect(() => {
		getSession()
			.then(async (session) => {
				if (session.state === "active") {
					navigate("/");
				} else if (session.state === "disabled") {
					navigate("/users");
				} else if (session.state === "pending_2fa") {
					setStep("otp");
				} else {
					setSetup(await start2FASetup());
					setStep("setup");
				}
			})
			.catch(() => undefined);
	}, [navigate]);

	const completeLogin = () => {
		clearClientSession();
		navigate("/");
	};

	const openRequiredSetup = async () => {
		setSetup(await start2FASetup());
		setStep("setup");
	};

	const login = async (values: LoginFormValues) => {
		setError("");
		try {
			const session = await createSession(values.username, values.password);
			if (session.state === "pending_2fa") {
				setStep("otp");
				setOTP("");
			} else if (session.state === "setup_required") {
				await openRequiredSetup();
			} else if (session.state === "disabled") {
				clearClientSession();
				navigate("/users");
			} else {
				completeLogin();
			}
		} catch (err: any) {
			setError(err.response?._data?.detail || "Login failed");
		}
	};

	const submitOTP = async () => {
		setError("");
		setChallengeLoading(true);
		try {
			await verify2FA(otp);
			completeLogin();
		} catch (err: any) {
			setError(err.response?._data?.detail || "Invalid authentication code");
		} finally {
			setChallengeLoading(false);
		}
	};

	const confirmSetup = async () => {
		setError("");
		setChallengeLoading(true);
		try {
			await confirm2FASetup(otp);
			completeLogin();
		} catch (err: any) {
			setError(err.response?._data?.detail || "Invalid authentication code");
		} finally {
			setChallengeLoading(false);
		}
	};

	const cancelChallenge = async () => {
		try {
			await logout();
		} finally {
			setStep("credentials");
			setOTP("");
			setSetup(null);
			setError("");
		}
	};

	const handleInvalid = async (_errors: FieldErrors<LoginFormValues>) => {
		setError("");
	};

	const passwordToggle = (
		<IconButton
			aria-label={
				showPassword
					? t("admins.hidePassword")
					: t("admins.showPassword")
			}
			color={mutedColor}
			icon={showPassword ? <EyeSlash /> : <Eye />}
			onClick={() => setShowPassword((visible) => !visible)}
			onMouseDown={(event) => event.preventDefault()}
			size="sm"
			variant="ghost"
			_hover={{ bg: "transparent", color: textColor }}
		/>
	);

	return (
		<Box
			alignItems="stretch"
			bg={pageBg}
			display="grid"
			gridTemplateColumns={{
				base: "minmax(0, 420px)",
				lg: "minmax(0, 1fr) minmax(360px, 430px)",
			}}
			gap={{ base: 6, lg: 16 }}
			justifyContent="center"
			minH="100dvh"
			maxW="1120px"
			mx="auto"
			px={{ base: 4, md: 10, lg: 12 }}
			py={{ base: 6, md: 10 }}
			w="full"
		>
			<VStack
				align="stretch"
				display={{ base: "none", lg: "flex" }}
				justify="center"
				pb={12}
				spacing={10}
			>
				<HStack align="center" spacing={5}>
					<Box
						alignItems="center"
						bg={elevatedBg}
						borderColor={borderColor}
						borderRadius="18px"
						borderWidth="1px"
						display="inline-flex"
						flexShrink={0}
						h={24}
						justifyContent="center"
						w={24}
					>
						<LogoIcon alt="AntiMage" h={20} w={20} src={logoUrl} />
					</Box>
					<VStack align="start" spacing={1}>
						<Text
							color={textColor}
							fontSize={{ lg: "3xl", xl: "4xl" }}
							fontWeight="800"
							lineHeight="1"
						>
							AntiMage
						</Text>
						<Text
							color={signalColor}
							fontSize="xs"
							fontWeight="800"
							letterSpacing="0.18em"
						>
							CONTROL PLANE
						</Text>
					</VStack>
				</HStack>

				<Box borderInlineStartWidth="2px" borderColor={accentColor} ps={7}>
					<Text color={textColor} fontSize="2xl" fontWeight="700" lineHeight="1.25" maxW="460px">
						Private infrastructure, under your control.
					</Text>
					<Text color={mutedColor} fontSize="md" lineHeight="1.8" mt={4} maxW="450px">
						Manage access, nodes, services, and traffic from one focused operator console.
					</Text>
				</Box>

				<HStack aria-hidden="true" spacing={1}>
					{[
						{ color: accentColor, width: "26%" },
						{ color: signalColor, width: "42%" },
						{ color: signalColor, width: "68%" },
						{ color: accentColor, width: "34%" },
						{ color: signalColor, width: "84%" },
						{ color: signalColor, width: "51%" },
						{ color: accentColor, width: "72%" },
						{ color: signalColor, width: "38%" },
					].map((bar) => (
						<Box
							key={bar.width}
							bg={bar.color}
							h="3px"
							opacity={bar.color === accentColor ? 0.9 : 0.55}
							w={bar.width}
						/>
					))}
				</HStack>
			</VStack>

			<VStack justify="center" spacing={4} w="full">
				<HStack alignSelf="flex-end" flexShrink={0} spacing={2}>
					<Language triggerVariant="ghost" />
					<LoginThemeMenu />
				</HStack>
				<Box
					bg={surfaceBg}
					borderColor={borderColor}
					borderRadius="12px"
					borderTopColor={accentColor}
					borderTopWidth="3px"
					borderWidth="1px"
					boxShadow="0 24px 72px rgba(0, 0, 0, 0.28)"
					p={{ base: 5, sm: 7 }}
					w="full"
				>
					<HStack justifyContent="space-between" mb={7} spacing={3}>
						<HStack color={textColor} minW={0} spacing={3}>
							<Box
								alignItems="center"
								bg={elevatedBg}
								borderColor={borderColor}
								borderRadius="10px"
								borderWidth="1px"
								display="inline-flex"
								flexShrink={0}
								h={10}
								justifyContent="center"
								w={10}
							>
								<LogoIcon alt={t("menu")} src={logoUrl} />
							</Box>
							<VStack align="start" minW={0} spacing={0}>
								<Text fontSize="md" fontWeight="800" noOfLines={1}>
									AntiMage
								</Text>
								<Text color={signalColor} fontSize="9px" fontWeight="800" letterSpacing="0.12em">
									ADMIN ACCESS
								</Text>
							</VStack>
						</HStack>
					</HStack>

					<VStack align="stretch" spacing={1} textAlign="center">
						<Text color={textColor} fontSize="lg" fontWeight="800">
							{step === "credentials"
								? t("login.welcome")
								: step === "otp"
									? t("login.twoFactorTitle")
									: t("login.setupTwoFactorTitle")}
						</Text>
						<Text color={mutedColor} fontSize="sm">
							{step === "credentials"
								? t("login.welcomeBack")
								: step === "otp"
									? t("login.twoFactorHint")
									: t("login.setupTwoFactorHint")}
						</Text>
					</VStack>

					<Box mt={6}>
						{step === "credentials" ? (
						<form onSubmit={handleSubmit(login, handleInvalid)}>
							<VStack spacing={4}>
								<LoginField
									autoComplete="username"
									dir={dir}
									errorMessage={
										errors.username?.message
											? t(errors.username.message as string)
											: undefined
									}
									icon={<User />}
									label={t("username")}
									placeholder={t("username")}
									registration={register("username")}
								/>
								<LoginField
									autoComplete="current-password"
									dir={dir}
									endElement={passwordToggle}
									errorMessage={
										errors.password?.message
											? t(errors.password.message as string)
											: undefined
									}
									icon={<Lock />}
									label={t("password")}
									placeholder={t("password")}
									registration={register("password")}
									type={showPassword ? "text" : "password"}
								/>

								{error && (
									<Alert
										borderRadius="8px"
										fontSize="sm"
										status="error"
										variant="left-accent"
										w="full"
									>
										<AlertIcon />
										<AlertDescription>{error}</AlertDescription>
									</Alert>
								)}

								<Button
									bg={accentColor}
									borderRadius="8px"
									color="white"
									h="44px"
									isDisabled={!canSubmit}
									isLoading={isSubmitting}
									leftIcon={<LoginIcon />}
									mt={1}
									type="submit"
									w="full"
									_hover={{ bg: "var(--am-panel-accent-hover)" }}
									_active={{ transform: "translateY(1px)" }}
								>
									{t("login")}
								</Button>
							</VStack>
						</form>
						) : (
							<VStack spacing={4}>
								{step === "setup" && setup && (
									<>
										<Box bg="white" borderRadius="8px" p={3}>
											<QRCodeCanvas value={setup.uri} size={180} />
										</Box>
										<Text color={mutedColor} fontFamily="mono" fontSize="xs" wordBreak="break-all">
											{setup.secret}
										</Text>
									</>
								)}
								<FormControl>
									<FormLabel>{t("login.authenticationCode")}</FormLabel>
									<CInput
										autoComplete="one-time-code"
										inputMode="numeric"
										maxLength={6}
										textAlign="center"
										value={otp}
										onChange={(event) => setOTP(event.target.value.replace(/\D/g, ""))}
									/>
								</FormControl>
								{error && (
									<Alert borderRadius="8px" fontSize="sm" status="error" variant="left-accent" w="full">
										<AlertIcon />
										<AlertDescription>{error}</AlertDescription>
									</Alert>
								)}
								<Button
									bg={accentColor}
									color="white"
									h="44px"
									isDisabled={otp.length !== 6}
									isLoading={challengeLoading}
									onClick={step === "otp" ? submitOTP : confirmSetup}
									w="full"
								>
									{t("continue")}
								</Button>
								<Button onClick={cancelChallenge} variant="ghost" w="full">
									{t("back")}
								</Button>
							</VStack>
						)}
					</Box>
				</Box>
			</VStack>
		</Box>
	);
};

export default Login;
