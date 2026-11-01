import { Box, Center, Text, VStack, useColorModeValue } from "@chakra-ui/react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useHref, useSearchParams } from "react-router-dom";
import { PageHeader } from "components/ui";
import { getTutorialsUrl } from "utils/tutorials";

export const TutorialsPage = () => {
	const { t, i18n } = useTranslation();
	const dashboardRoot = useHref("/");
	const [searchParams] = useSearchParams();
	const [loadedFrameSrc, setLoadedFrameSrc] = useState("");
	const borderColor = useColorModeValue("panel.border", "panel.border");
	const frameBg = useColorModeValue("panel.surface", "panel.surface");
	const activeLanguage = i18n.resolvedLanguage || i18n.language;
	const frameSrc = getTutorialsUrl(
		dashboardRoot,
		activeLanguage,
		searchParams.get("doc") || "",
	);
	const isLoaded = loadedFrameSrc === frameSrc;
	return (
		<VStack spacing={4} align="stretch">
			<PageHeader title={t("tutorials.menu")} />
			<Box
				borderWidth="1px"
				borderColor={borderColor}
				borderRadius="lg"
				overflow="hidden"
				position="relative"
				h={{ base: "calc(100dvh - 216px)", md: "calc(100dvh - 208px)" }}
				minH={{ base: "360px", md: "500px" }}
				bg={frameBg}
			>
				{!isLoaded ? (
					<Center position="absolute" inset={0} bg={frameBg}>
						<Text color="panel.textMuted">{t("loading")}...</Text>
					</Center>
				) : null}
				<Box
					as="iframe"
					key={frameSrc}
					title={t("tutorials.menu")}
					src={frameSrc}
					w="100%"
					h="100%"
					border="0"
					bg={frameBg}
					opacity={isLoaded ? 1 : 0}
					onLoad={() => setLoadedFrameSrc(frameSrc)}
				/>
			</Box>
		</VStack>
	);
};
