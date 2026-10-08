import {
	Badge,
	Box,
	Button,
	Drawer,
	DrawerBody,
	DrawerContent,
	DrawerOverlay,
	Flex,
	HStack,
	Icon,
	IconButton,
	Text,
	useBreakpointValue,
	useColorMode,
	useDisclosure,
} from "@chakra-ui/react";
import { Bars3Icon, MoonIcon, SunIcon } from "@heroicons/react/24/outline";
import useGetUser from "hooks/useGetUser";
import { useEffect, useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { Link, Outlet, useLocation, useNavigate } from "react-router-dom";
import { logout } from "service/auth";
import { clearClientSession } from "utils/session";
import { updateThemeColor } from "utils/themeColor";
import { AMNavigation } from "./AMNavigation";
import {
	readNavigationMode,
	type NavigationMode,
	writeNavigationMode,
} from "./navigationModel";

const pageTitleKeys: Record<string, string> = {
	"/": "am.nav.overview",
	"/users": "am.nav.users",
	"/services": "am.nav.services",
	"/node-settings": "am.nav.nodes",
	"/hosts": "am.nav.hosts",
	"/usage": "am.nav.usage",
	"/admins": "am.nav.admins",
	"/xray-settings": "am.nav.xray",
	"/xray-logs": "am.nav.xrayLogs",
	"/access-insights": "am.nav.accessInsights",
	"/recent-actions": "am.nav.eventHistory",
	"/settings": "am.nav.settings",
	"/diagnostics": "am.nav.diagnostics",
	"/updates": "am.nav.updates",
	"/operations": "am.nav.operationHistory",
	"/bulk-actions": "am.nav.bulkActions",
	"/myaccount": "am.nav.myAccount",
	"/tutorials": "am.nav.guides",
	"/haproxy": "am.nav.loadBalancing",
	"/external-apps": "am.nav.externalApps",
	"/api-docs": "am.nav.apiDocs",
	"/phpmyadmin": "phpmyadmin.menu",
};

export function AMShell() {
	const { userData } = useGetUser();
	const { t, i18n } = useTranslation();
	const { pathname } = useLocation();
	const navigate = useNavigate();
	const isMobile = useBreakpointValue({ base: true, lg: false }) ?? false;
	const drawer = useDisclosure();
	const { colorMode, toggleColorMode } = useColorMode();
	const [mode, setMode] = useState<NavigationMode>(() => readNavigationMode(""));
	const [modeReady, setModeReady] = useState(false);
	const isRTL = i18n.dir(i18n.language) === "rtl";
	const username = userData.username || "admin";
	const titleKey = pageTitleKeys[pathname] ?? "am.nav.overview";
	const nativeTheme = (colorMode === "light"
		? { "--am-native-canvas":"#edf3f4", "--am-native-rail":"#f7fbfb", "--am-native-surface":"#ffffff", "--am-native-border":"#d2dfe1", "--am-native-text":"#14272e", "--am-native-secondary":"#39545c", "--am-native-muted":"#657d84", "--am-native-accent":"#087b80", "--am-native-active":"#e0f1ef" }
		: { "--am-native-canvas":"#08131a", "--am-native-rail":"#0b1922", "--am-native-surface":"#0f222c", "--am-native-border":"rgba(145,174,184,.17)", "--am-native-text":"#edf6f5", "--am-native-secondary":"#c0d2d3", "--am-native-muted":"#829aa1", "--am-native-accent":"#46dcc6", "--am-native-active":"#142c34" }) as CSSProperties;
	const mainBg = "var(--am-native-canvas)";
	const railBg = "var(--am-native-rail)";
	const surfaceBg = "var(--am-native-surface)";
	const borderColor = "var(--am-native-border)";

	useEffect(() => {
		setMode(readNavigationMode(username));
		setModeReady(true);
	}, [username]);

	useEffect(() => {
		if (modeReady) writeNavigationMode(username, mode);
	}, [mode, modeReady, username]);

	useEffect(() => {
		updateThemeColor(colorMode === "light" ? "light" : "dark");
	}, [colorMode]);

	const modeLabel = useMemo(
		() => t(mode === "simple" ? "am.nav.simpleMode" : "am.nav.advancedMode"),
		[mode, t],
	);

	const handleLogout = async () => {
		try {
			await logout();
		} finally {
			clearClientSession();
			navigate("/login", { replace: true });
		}
	};

	const navigation = (
		<AMNavigation
			admin={userData}
			mode={mode}
			onNavigate={isMobile ? drawer.onClose : undefined}
		/>
	);

	return (
		<Flex className="am-native-shell" style={nativeTheme} minH="100vh" bg={mainBg} color="var(--am-native-text)" dir={isRTL ? "rtl" : "ltr"}>
			{!isMobile && (
				<Box
					as="aside"
					w="276px"
					minH="100vh"
					position="sticky"
					top={0}
					alignSelf="flex-start"
					bg={railBg}
					borderInlineEnd="1px solid"
					borderColor={borderColor}
					px={4}
					py={5}
				>
					<HStack spacing={3} px={3} mb={8}>
						<Box w="10px" h="34px" bg="var(--am-native-accent)" borderRadius="full" />
						<Box>
							<Text fontSize="lg" fontWeight="bold" letterSpacing="-.03em">AntiMage</Text>
						<Text fontSize="xs" color="var(--am-native-muted)">{t("am.shell.productLine")}</Text>
						</Box>
					</HStack>
					{navigation}
					<Box mt={8} pt={4} borderTop="1px solid" borderColor={borderColor}>
						<Text px={3} fontSize="xs" color="var(--am-native-muted)">
							{t("am.shell.signedInAs")}
						</Text>
						<Text px={3} pt={1} fontSize="sm" fontWeight="semibold" noOfLines={1}>
							{username}
						</Text>
					</Box>
				</Box>
			)}

			<Flex as="main" direction="column" flex="1" minW={0} minH="100vh">
				<HStack
					as="header"
					minH={{ base: "64px", md: "72px" }}
					px={{ base: 4, xl: 8 }}
					borderBottom="1px solid"
					borderColor={borderColor}
					bg={surfaceBg}
					justify="space-between"
					position="sticky"
					top={0}
					zIndex={10}
				>
					<HStack spacing={3} minW={0}>
						{isMobile && (
							<IconButton
								aria-label={t("am.nav.openMenu")}
								icon={<Icon as={Bars3Icon} boxSize={5} />}
								variant="ghost"
								onClick={drawer.onOpen}
							/>
						)}
						<Box minW={0}>
							<Text fontSize="xs" color="var(--am-native-muted)" noOfLines={1}>
								{t("am.nav.commandCenter")}
							</Text>
							<Text fontSize={{ base: "md", md: "lg" }} fontWeight="bold" noOfLines={1}>
								{t(titleKey)}
							</Text>
						</Box>
					</HStack>
					<HStack spacing={{ base: 1, md: 2 }} flexShrink={0}>
						<Button
							size="sm"
							variant="outline"
							aria-label={t("am.nav.toggleMode")}
							title={t("am.nav.toggleMode")}
							onClick={() => setMode(mode === "simple" ? "advanced" : "simple")}
						>
							<Badge colorScheme={mode === "advanced" ? "purple" : "gray"} mr={2}>{modeLabel}</Badge>
							{t("am.nav.mode")}
						</Button>
						<IconButton
							aria-label={t(colorMode === "light" ? "am.shell.darkMode" : "am.shell.lightMode")}
							icon={<Icon as={colorMode === "light" ? MoonIcon : SunIcon} boxSize={5} />}
							variant="ghost"
							onClick={toggleColorMode}
						/>
						<Button
							size="sm"
							variant="ghost"
							aria-label={t("am.shell.changeLanguage")}
							onClick={() => void i18n.changeLanguage(i18n.language === "fa" ? "en" : "fa")}
						>
							{i18n.language === "fa" ? "EN" : "FA"}
						</Button>
						<Button as={Link} to="/myaccount" size="sm" variant="ghost" display={{ base: "none", sm: "inline-flex" }}>
							{username}
						</Button>
						<Button size="sm" variant="outline" onClick={() => void handleLogout()}>
							{t("am.shell.signOut")}
						</Button>
					</HStack>
				</HStack>
				<Box as="section" flex="1" minH={0} overflowY="auto" px={{ base: 4, xl: 8 }} py={{ base: 5, xl: 8 }}>
					<Outlet />
				</Box>
			</Flex>

			<Drawer isOpen={drawer.isOpen} placement={isRTL ? "right" : "left"} onClose={drawer.onClose}>
				<DrawerOverlay />
				<DrawerContent bg={railBg} borderColor={borderColor}>
					<DrawerBody px={4} py={5}>
						<HStack spacing={3} px={3} mb={8}>
							<Box w="10px" h="34px" bg="var(--am-native-accent)" borderRadius="full" />
							<Box>
								<Text fontSize="lg" fontWeight="bold">AntiMage</Text>
							<Text fontSize="xs" color="var(--am-native-muted)">{t("am.shell.productLine")}</Text>
							</Box>
						</HStack>
						{navigation}
					</DrawerBody>
				</DrawerContent>
			</Drawer>
		</Flex>
	);
}
