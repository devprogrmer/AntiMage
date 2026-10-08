import { Box, HStack, Icon, Text, VStack } from "@chakra-ui/react";
import {
	ArrowsRightLeftIcon,
	BoltIcon,
	ChartBarIcon,
	CircleStackIcon,
	ClockIcon,
	CodeBracketSquareIcon,
	Cog6ToothIcon,
	DocumentTextIcon,
	EyeIcon,
	HomeIcon,
	LinkIcon,
	ServerStackIcon,
	Squares2X2Icon,
	UserCircleIcon,
	UserGroupIcon,
	WrenchScrewdriverIcon,
} from "@heroicons/react/24/outline";
import { NavLink, useLocation } from "react-router-dom";
import { buildAMNavigation, type NavigationMode } from "./navigationModel";
import type { UserApi } from "types/User";
import { useTranslation } from "react-i18next";

const iconFor: Record<string, typeof HomeIcon> = {
	overview: HomeIcon,
	users: UserGroupIcon,
	"bulk-actions": BoltIcon,
	admins: UserGroupIcon,
	"my-account": UserCircleIcon,
	services: Squares2X2Icon,
	nodes: ServerStackIcon,
	"load-balancing": ArrowsRightLeftIcon,
	usage: ChartBarIcon,
	hosts: LinkIcon,
	xray: WrenchScrewdriverIcon,
	"xray-logs": DocumentTextIcon,
	"access-insights": EyeIcon,
	health: ChartBarIcon,
	diagnostics: CircleStackIcon,
	updates: ClockIcon,
	"recent-actions": ClockIcon,
	settings: Cog6ToothIcon,
	"external-apps": CircleStackIcon,
	"api-docs": CodeBracketSquareIcon,
	tutorials: DocumentTextIcon,
};

type Props = {
	admin: UserApi;
	mode: NavigationMode;
	onNavigate?: () => void;
	compact?: boolean;
};

export function AMNavigation({ admin, mode, onNavigate, compact = false }: Props) {
	const { t } = useTranslation();
	const { pathname } = useLocation();
	const groups = buildAMNavigation(admin, mode);
	const muted = "var(--am-native-muted)";
	const border = "var(--am-native-border)";
	const active = "var(--am-native-active)";
	const accent = "var(--am-native-accent)";

	return (
		<VStack as="nav" aria-label={t("am.nav.ariaLabel")} align="stretch" spacing={6}>
			{groups.map((group) => (
				<Box as="section" key={group.id} aria-labelledby={`am-nav-${group.id}`}>
					<Text
						id={`am-nav-${group.id}`}
						px={3}
						mb={2}
						fontSize="xs"
						fontWeight="bold"
						letterSpacing="0.08em"
						textTransform="uppercase"
						color={muted}
					>
						{t(group.labelKey)}
					</Text>
					<VStack align="stretch" spacing={1}>
						{group.items.map((item) => {
							const selected =
								item.path === "/"
									? pathname === "/"
									: pathname === item.path || pathname.startsWith(`${item.path}/`);
							const ItemIcon = iconFor[item.id] ?? CircleStackIcon;
							return (
								<NavLink
									key={`${group.id}-${item.id}`}
									to={item.path}
									end={item.path === "/"}
									onClick={onNavigate}
									aria-label={t(item.labelKey)}
								>
									<HStack
										minH="44px"
										px={3}
										borderRadius="lg"
										borderInlineStart="3px solid"
										borderInlineStartColor={selected ? accent : "transparent"}
										bg={selected ? active : "transparent"}
									color={selected ? "var(--am-native-text)" : "var(--am-native-secondary)"}
										fontWeight={selected ? "semibold" : "medium"}
										_hover={{ bg: active, color: "var(--am-native-text)" }}
										_focusVisible={{ outline: `2px solid ${accent}`, outlineOffset: "2px" }}
										transition="background 120ms ease, color 120ms ease"
										spacing={3}
									>
										<Icon as={ItemIcon} boxSize={5} flexShrink={0} aria-hidden="true" />
										{!compact && (
											<Text fontSize="sm" noOfLines={1}>
												{t(item.labelKey)}
											</Text>
										)}
									</HStack>
								</NavLink>
							);
						})}
					</VStack>
					<Box mt={group.id === "settings" ? 0 : 5} borderBottom="1px solid" borderColor={border} />
				</Box>
			))}
		</VStack>
	);
}
